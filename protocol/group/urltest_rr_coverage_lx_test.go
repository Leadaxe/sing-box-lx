// lx:begin SPEC 138 round_robin self-heal: the rules of ALGORITHM.md one by one

package group

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	tun "github.com/sagernet/sing-tun"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// countsOf returns the slot counters of tag.
func countsOf(b *balancer, tag string) (conns uint32, fails uint32) {
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	if c := m.counts[tag]; c != nil {
		return c.conns, c.fails
	}
	return 0, 0
}

// checkAsync runs one check of the pool in the background.
func (f *healFixture) checkAsync() chan struct{} {
	f.s.balancer.heal.holdRound()
	done := make(chan struct{})
	go func() {
		f.s.rrHealCheck()
		close(done)
	}()
	return done
}

func waitClosed(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the check is still running")
	}
}

// --- D1, D3, D4 ----------------------------------------------------------------------

// Scenario 22, D1: the probes of a fail, the control before and after answers, but
// an ANSWER via a that started at or after x came in during the check — no verdict.
// Without it, a is struck out.
func TestRRCov_D1_answerSinceTheStartKeepsTheNode(t *testing.T) {
	for _, answered := range []bool{true, false} {
		f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
		f.nodes["a"].probeFails.Store(100)
		f.nodes["a"].slow = 150 * time.Millisecond
		done := f.checkAsync()
		time.Sleep(30 * time.Millisecond)
		if answered {
			now := rrClock(time.Now())
			f.s.rrProof(f.s.balancer.trackOf("a"), now, now) // traffic through a answered
		}
		waitClosed(t, done)
		if kept := f.g.history.LoadURLTestHistory("a") != nil; kept != answered {
			t.Fatalf("ANSWER via a since x %v: a kept %v", answered, kept)
		}
	}
}

// D1: a history written after x — a scheduled run answered for the node — is newer
// than the verdict: the node is not struck out.
func TestRRCov_D1_newerHistoryKeepsTheNode(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
	f.nodes["a"].probeFails.Store(100)
	f.nodes["a"].slow = 150 * time.Millisecond
	done := f.checkAsync()
	time.Sleep(30 * time.Millisecond)
	f.fresh("a")
	waitClosed(t, done)
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b") {
		t.Fatal("a history newer than x keeps a")
	}
}

// D4: the control after the decisive probe has no ANSWER — no verdict on the silent
// node.
func TestRRCov_D4_controlAfterSilentNoVerdict(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
	f.nodes["c"].dead.Store(true)
	f.nodes["a"].dead.Store(true)
	f.nodes["a"].slow = 150 * time.Millisecond
	done := f.checkAsync()
	waitUntil(t, func() bool { return f.probes("a") == 2 }) // the decisive probe runs
	f.nodes["b"].dead.Store(true)
	waitClosed(t, done)
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b") || f.probes("b") != 2 {
		t.Fatalf("D4: no verdict; probes of b %d", f.probes("b"))
	}
}

// Scenario 18, С4: the whole pool on one dead server. Its first probes fail at
// once, the batch of candidates starts at once, the first answer is the control;
// every pool node is struck out and replaced by one check.
func TestRRCov_18_wholePoolStruckInOneCheck(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e", "f"}, []string{"a", "b", "c"}, 0)
	for _, tag := range []string{"a", "b", "c"} {
		f.nodes[tag].dead.Store(true)
	}
	f.runCheck()
	pool := append([]string(nil), f.pool()...)
	sort.Strings(pool)
	if !samePool(pool, "d", "e", "f") || f.allHistory("a") || f.allHistory("b") || f.allHistory("c") {
		t.Fatalf("one check strikes and replaces the pool: %v", f.pool())
	}
}

// Scenarios 5 and 6: the control answers late. A node whose decisive probe after it
// answers stays (D3); a dead one is struck out on a slow network (D1).
func TestRRCov_5_6_lateControl(t *testing.T) {
	late := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	late.nodes["a"].probeFails.Store(1)
	late.nodes["b"].slow = 150 * time.Millisecond
	late.runCheck()
	if !late.allHistory("a") || late.probes("a") != 2 {
		t.Fatal("scenario 5: the decisive probe after the late control answers, D3")
	}
	slow := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	slow.nodes["a"].dead.Store(true)
	slow.nodes["b"].slow = 150 * time.Millisecond
	slow.runCheck()
	if slow.allHistory("a") {
		t.Fatal("scenario 6: a dead node is struck out on a slow network")
	}
}

// Scenarios 28 and 34 (A6 broken): a node that fails both probes of a check while
// the control answers before and after is struck out though alive. With no live
// candidate it stays in its slot, struck; the next check does not judge it again,
// probes it as a candidate, and its answer keeps its slot alive.
func TestRRCov_28_34_struckLiveNodeComesBackAsCandidate(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
	f.nodes["a"].probeFails.Store(2)
	f.nodes["c"].dead.Store(true)
	f.runCheck()
	track := f.s.balancer.trackOf("a")
	if f.allHistory("a") || track != nil || !samePool(f.pool(), "", "b") || f.s.balancer.memory.strikes["a"] != 1 {
		t.Fatal("D1: a struck out; it leaves its slot, empty with no replacement (R1)")
	}
	before := f.probes("b") // the control and the control after of the check
	f.runSearch()
	if !f.allHistory("a") || !samePool(f.pool(), "a", "b") || f.probes("a") != 3 || f.probes("b") != before {
		t.Fatalf("the search: a answered as a candidate and takes the empty slot, b is not probed: probes %d/%d", f.probes("a"), f.probes("b")-before)
	}
}

// --- L3 ------------------------------------------------------------------------------

// L3, W1: one round at a time, the next one not earlier than the pause after the
// end of the previous; a freeze leaves no pause behind, the first reason after it
// gets a round at once.
func TestRRCov_L3_oneCheckAndTheGap(t *testing.T) {
	var heal rrHeal
	if !heal.beginRound(time.Now()) || heal.beginRound(time.Now()) {
		t.Fatal("one check at a time")
	}
	heal.endRound(time.Now(), true, true, false)
	if heal.beginRound(time.Now()) {
		t.Fatal("the gap after a check")
	}
	past := time.Now().Add(-rrHealGap - time.Second)
	heal.setPauseEnd(&past)
	if !heal.beginRound(time.Now()) {
		t.Fatal("the gap is over")
	}
	heal.freeze()
	heal.endRound(time.Now(), true, false, true)
	if heal.pauseEnd() != nil || heal.ready(time.Now()) {
		t.Fatal("a check cut by a freeze leaves no gap; nothing starts in the freeze")
	}
	heal.epochEnd.Store(rrClock(time.Now()))
	if !heal.ready(time.Now()) {
		t.Fatal("the first check after the freeze at once")
	}
}

// L3, §4.2: the limits of the life probe — one in flight per node, one per node
// every 3 s, four per group in 15 s, by a UDP wait one per group a minute.
func TestRRCov_L3_lifeProbeLimits(t *testing.T) {
	var heal rrHeal
	tracks := []*rrTrack{newRRTrack("a"), newRRTrack("b"), newRRTrack("c"), newRRTrack("d"), newRRTrack("e")}
	now := int64(time.Hour)
	if !heal.claimLife(tracks[0], now, false) || heal.claimLife(tracks[0], now+int64(rrLifeGap), false) {
		t.Fatal("one in flight per node")
	}
	tracks[0].lifeBusy.Store(false)
	if heal.claimLife(tracks[0], now+int64(rrLifeGap)-1, false) || !heal.claimLife(tracks[0], now+int64(rrLifeGap), false) {
		t.Fatal("one per node every 3 s")
	}
	if !heal.claimLife(tracks[1], now, false) || !heal.claimLife(tracks[2], now, false) || heal.claimLife(tracks[3], now, false) {
		t.Fatal("four per group in 15 s")
	}
	if !heal.claimLife(tracks[3], now+int64(rrLifeWindow), true) || heal.claimLife(tracks[4], now+2*int64(rrLifeWindow), true) {
		t.Fatal("a UDP wait: one per group a minute")
	}
	if !heal.claimLife(tracks[4], now+int64(rrLifeWindow)+int64(rrUDPLifeGap), true) {
		t.Fatal("the next UDP life probe after a minute")
	}
}

// L3, §5: k batches of candidates per check, the replacement included. Two dead
// pool nodes (old history, still in their slots: pool nodes, not candidates) and dead
// candidates with no history: the check probes the pool and spends its k batches of
// |P|; the replacement adds none.
func TestRRCov_L3_oneKPerCheck(t *testing.T) {
	tags := []string{"a", "b", "c1", "c2", "c3", "c4"}
	f := newHealFixture(t, tags, []string{"a", "b"}, 0)
	for _, tag := range tags {
		f.nodes[tag].dead.Store(true)
		if tag != "a" && tag != "b" {
			f.g.history.DeleteURLTestHistory(tag)
		}
	}
	f.runCheck()
	total := 0
	for _, tag := range tags {
		total += int(f.probes(tag))
	}
	if total != 2+2*rrCandidateBatches || f.probes("c4") != 1 || f.s.balancer.heal.cursor != 0 {
		t.Fatalf("k batches of 2 per check: %d probes, cursor %d", total, f.s.balancer.heal.cursor)
	}
}

// The full run after a network change aborts the check in flight and waits for it
// to end (it may hold the run's flag).
func TestRRCov_networkRunAbortsTheCheck(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	hold := make(chan struct{})
	defer close(hold)
	f.nodes["a"].hold, f.nodes["b"].hold = hold, hold
	if !f.s.rrStartRound(f.s.rrHealCheck) {
		t.Fatal("precondition: the check starts")
	}
	waitUntil(t, func() bool { return f.probes("a") == 1 && f.probes("b") == 1 })
	done := make(chan struct{})
	go func() {
		f.s.rrNetworkRun()
		close(done)
	}()
	waitClosed(t, done)
	if f.s.balancer.heal.roundBusy() {
		t.Fatal("the check ended before the run")
	}
}

// --- §5: the fill during a run, the retry -------------------------------------------

// §5, I3′, I5: a node that answered its probe during a run takes a slot whose node
// has no history or an empty one; a live slot, a node in the pool and a quarantined
// node do not move.
func TestRRCov_fillOnAlive(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b"}, 0)
	f.g.history.DeleteURLTestHistory("a")
	quarantine := map[string]bool{"e": true}
	f.s.balancer.memory.quarantine.Store(&quarantine)
	f.g.rrFillOnAlive("e")
	f.g.rrFillOnAlive("b")
	if !samePool(f.pool(), "a", "b") {
		t.Fatal("a quarantined node and a pool member take no slot")
	}
	f.g.rrFillOnAlive("c")
	if !samePool(f.pool(), "c", "b") {
		t.Fatalf("c takes the dead slot of a: %v", f.pool())
	}
	f.g.rrFillOnAlive("d")
	if !samePool(f.pool(), "c", "b") {
		t.Fatal("live slots stay as they are")
	}
}

// §5 (SPEC 019): a dead starting pool of 60 nodes, the last three alive. The first
// one to answer takes a dead slot while the run still probes; the end of the run
// sets the pool. A healthy pool stays as it is.
func TestRRCov_fillDuringTheRun(t *testing.T) {
	tags := make([]string, 60)
	for i := range tags {
		tags[i] = fmt.Sprintf("n%02d", i)
	}
	f := newHealFixture(t, tags, tags[:3], 0)
	for i, tag := range tags {
		f.g.history.DeleteURLTestHistory(tag)
		if i < 57 {
			f.nodes[tag].dead.Store(true)
		}
	}
	f.nodes["n57"].slow = 50 * time.Millisecond
	f.nodes["n58"].slow = 400 * time.Millisecond
	f.nodes["n59"].slow = 800 * time.Millisecond
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f.s.selectBalanced(context.Background(), N.NetworkTCP, siteAddr("site.example"))
		}
	}()
	done := make(chan struct{})
	go func() {
		f.g.CheckOutbounds(f.g.ctx, false)
		close(done)
	}()
	waitUntil(t, func() bool { return slotIndex(f.g.balancer.slotsSnapshot(), "n57") >= 0 })
	select {
	case <-done:
		t.Fatal("n57 takes a slot while the run still probes, not at its end")
	default:
	}
	<-done
	close(stop)
	wg.Wait()
	for _, tag := range []string{"n57", "n58", "n59"} {
		if slotIndex(f.g.balancer.slotsSnapshot(), tag) < 0 {
			t.Fatalf("the end of the run: %s in the pool, got %v", tag, f.pool())
		}
	}
	healthy := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	healthy.g.CheckOutbounds(healthy.g.ctx, false)
	if !samePool(healthy.pool(), "a", "b", "c") {
		t.Fatalf("a healthy pool stays as it is, got %v", healthy.pool())
	}
}

// §5: the retry takes the next slot under no suspicion; with none, the next one
// under suspicion; never the failed one or a dead one.
func TestRRCov_retryPrefersSlotWithoutSuspicion(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}, 0)
	b := f.s.balancer
	now := rrClock(time.Now())
	b.trackOf("b").suspectAt(now, now)
	target := func() string {
		node := f.s.rrRetryTarget(b.slotsSnapshot(), 0, "a", N.NetworkTCP)
		if node == nil {
			return ""
		}
		return node.Tag()
	}
	if got := target(); got != "c" {
		t.Fatalf("the next slot without suspicion: %s", got)
	}
	b.trackOf("c").suspectAt(now, now)
	b.trackOf("d").suspectAt(now, now)
	if got := target(); got != "b" {
		t.Fatalf("every other slot under suspicion: the next one, got %s", got)
	}
	f.g.history.DeleteURLTestHistory("b")
	if got := target(); got != "c" {
		t.Fatalf("a dead slot is no retry: %s", got)
	}
}

// §4.3: a pick moved off a slot under suspicion still looks at that slot (its
// check request is served).
func TestRRCov_bypassedSlotLooked(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	track := f.s.balancer.trackOf("a")
	now := rrClock(time.Now())
	track.suspectAt(now, now)
	track.wantCheck.Store(true)
	f.s.balancer.heal.holdRound() // the check is not under test
	defer f.s.balancer.heal.dropRound()
	node, picked := f.s.selectBalanced(f.keyOn(t, "site.example", 0), N.NetworkTCP, siteAddr("site.example"))
	if node.Tag() == "a" || !picked.detour || track.lookAt.Load() == 0 {
		t.Fatal("the pick goes round a and looks at it")
	}
}

// §4.4: a sign on the slot of a detour shifts no key and writes no K2 memory — an
// explicit refusal (K1) and a soft sign alike; on the slot of the key it does.
func TestRRCov_signOnDetourShiftsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.s.balancer.heal.holdRound()
	defer f.s.balancer.heal.dropRound()
	ctx := f.keyOn(t, "site.example", 0)
	destination := siteAddr("site.example")
	refused := &adapter.DestinationRefusedLx{Tag: "a", Err: E.New("refused")}
	for _, failure := range []error{refused, E.New("reset")} {
		picked := f.s.testPick(ctx, destination, time.Now())
		picked.detour = true
		conn, _ := f.s.rrHealDial(ctx, N.NetworkTCP, destination, f.fb["a"], failure, picked)
		closeConn(conn)
	}
	if shift, _ := f.shift(ctx, "site.example"); shift != 0 || f.s.balancer.memory.k2.size.Load() != 0 {
		t.Fatal("a sign on the slot of a detour: no shift, no K2 record")
	}
	conn, _ := f.s.rrHealDial(ctx, N.NetworkTCP, destination, f.fb["a"], E.New("reset"), f.s.testPick(ctx, destination, time.Now()))
	closeConn(conn)
	if f.s.balancer.memory.k2.size.Load() != 1 {
		t.Fatal("on the slot of the key: the K2 record")
	}
}

// --- Q1, Q2 --------------------------------------------------------------------------

// Q1 for FAIL_NODE: the count needs both the ANSWER of the retry and an ANSWER via
// the node that started at or after the sign, within 2T; a sign made before E is
// dropped.
func TestRRCov_Q1_failNodeNeedsRetryAndProof(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	s, b := f.s, f.s.balancer
	track := b.trackOf("a")
	sign := func() (*rrSign, int64) {
		at := rrClock(time.Now())
		return s.rrFailSign(track, rrPick{start: time.Now()}, "a", at, true), at
	}
	fails := func() uint32 {
		_, n := countsOf(b, "a")
		return n
	}

	x, at := sign()
	x.add(rrSignRetry)
	s.rrProof(track, at-1, at) // started before the sign: no proof after it
	if fails() != 0 || track.pendingN.Load() != 1 {
		t.Fatal("the retry alone, or a proof begun before the sign, counts nothing")
	}
	s.rrProof(track, at, at+1)
	if fails() != 1 {
		t.Fatal("the retry and the proof after the sign: counted")
	}

	x, at = sign()
	s.rrProof(track, at+1, at+2)
	if fails() != 1 {
		t.Fatal("the proof alone counts nothing")
	}
	x.add(rrSignRetry)
	if fails() != 2 {
		t.Fatal("the retry after the proof: counted")
	}

	x, at = sign()
	x.add(rrSignRetry)
	s.rrProof(track, at+1, at+rrHorizon()+1)
	if fails() != 2 || track.pendingN.Load() != 0 {
		t.Fatal("a proof later than 2T after the sign: dropped, not counted")
	}

	x, at = sign()
	x.add(rrSignRetry)
	b.heal.epochEnd.Store(at + 1) // an event after the sign
	s.rrProof(track, at+2, at+3)
	if fails() != 2 || track.pendingN.Load() != 0 {
		t.Fatal("a sign made before E: dropped")
	}
}

// Q2: the median is taken over the busy (conns >= 8) and unquarantined nodes of
// the pool; the share has a floor of 5 %; a quarantine starts the counters of the
// node from zero.
func TestRRCov_Q2_medianFloorAndReset(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}, 0)
	b := f.s.balancer
	set := func(tag string, conns, fails uint32) {
		b.memory.access.Lock()
		c := b.memory.count(tag)
		c.conns, c.fails = conns, fails
		b.memory.access.Unlock()
	}
	set("a", 40, 7)
	set("b", 4, 4)  // not busy: out of the median
	set("c", 40, 8) // quarantined: out of the median
	set("d", 40, 0) // the median is 0
	quarantined := map[string]bool{"c": true}
	b.memory.quarantine.Store(&quarantined)
	if verdict := b.rrCountFail("a", rrKey{}); !verdict.quarantined || verdict.median != 0 {
		t.Fatalf("a: 8/40 against the median of d alone: %+v", verdict)
	}
	if conns, fails := countsOf(b, "a"); conns != 0 || fails != 0 {
		t.Fatal("Q2: the counters of a start from zero")
	}

	g := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	gb := g.s.balancer
	for tag, fails := range map[string]uint32{"a": 2, "b": 0} {
		gb.memory.access.Lock()
		c := gb.memory.count(tag)
		c.conns, c.fails = 80, fails
		gb.memory.access.Unlock()
	}
	if verdict := gb.rrCountFail("a", rrKey{}); verdict.quarantined {
		t.Fatal("3/80 is below the floor of 5 %")
	}
	if verdict := gb.rrCountFail("a", rrKey{valid: true, table: 1}); !verdict.quarantined {
		t.Fatal("4/80 reaches the floor against a median of 0")
	}
}

// Q2, §4.2: the median is taken over P, and a slot whose node has no history (D1
// of another group erased the shared leaf) is an empty slot, no node of P: its
// share stays out of the median even with counters left in it.
func TestRRCov_Q2_medianWithoutNodeWithoutHistory(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}, 0)
	b := f.s.balancer
	for tag, counts := range map[string][2]uint32{"a": {8, 3}, "b": {10, 3}, "c": {10, 3}, "d": {10, 0}} {
		b.memory.access.Lock()
		c := b.memory.count(tag)
		c.conns, c.fails = counts[0], counts[1]
		b.memory.access.Unlock()
	}
	f.g.history.DeleteURLTestHistory("b")
	// a: 4/8 = 0.5 against the median of {c, d} = 0.15; with b in it, 0.3, and no quarantine.
	if verdict := b.rrCountFail("a", rrKey{}); !verdict.quarantined || verdict.median != 0.15 {
		t.Fatalf("a: 4/8 against the median of c and d: %+v", verdict)
	}
}

// Q2 → §5: a quarantine erases the K2 records of the node and starts the check;
// the replacement puts a candidate into its slot.
func TestRRCov_Q2_quarantineReplaced(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	b := f.s.balancer
	for _, tag := range []string{"a", "b", "c"} {
		b.memory.access.Lock()
		b.memory.count(tag).conns = 20
		b.memory.access.Unlock()
	}
	now := rrClock(time.Now())
	f.s.rrSoftSign(rrKey{valid: true, table: 7}, 0, "a", now, now, false, true)
	for i := 0; i < rrQuarantineMinFails; i++ {
		f.s.rrCreditFail("a", rrKey{valid: true, table: uint64(100 + i)})
	}
	if !b.isQuarantined("a") || b.memory.k2.size.Load() != 0 {
		t.Fatal("quarantined, its K2 records erased")
	}
	f.waitCheck(t)
	if !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("the replacement takes the slot: %v", f.pool())
	}
}

// Scenario 16, С7-A: 20 % of the opens of a fail actively, different keys. The
// retry answers, the life probe answers — the slot is not bypassed; Q1 counts
// each failure once the probe proves the node; Q2 quarantines a at the third
// count, and the replacement takes its slot.
func TestRRCov_16_flakyNodeQuarantined(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	b := f.s.balancer
	for round := 0; round < 15 && !b.isQuarantined("a"); round++ {
		for slot := 0; slot < 3; slot++ {
			host := fmt.Sprintf("s%d-%d.example", round, slot)
			ctx := f.keyOn(t, host, slot)
			flaky := slot == 0 && round%5 == 0
			if flaky {
				f.fb["a"].dialErr = E.New("reset")
			}
			if _, answered := f.dial(t, ctx, host); !answered {
				t.Fatalf("round %d slot %d: the connection answers", round, slot)
			}
			f.fb["a"].dialErr = nil
			f.waitCheck(t)
			if flaky && b.trackOf("a") != nil && b.trackOf("a").suspect(rrClock(time.Now()), b.heal.epoch()) {
				t.Fatal("a fast refusal with an answering life probe: no bypass")
			}
		}
	}
	if !b.isQuarantined("a") {
		_, fails := countsOf(b, "a")
		t.Fatalf("Q2: a quarantined; fails %d", fails)
	}
	f.waitCheck(t)
	if !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("replaced: %v", f.pool())
	}
}

// Scenario 32: the server of a refuses everything with a code. The key shifts at
// once (K1); the life probe is refused too (S1) → the check → D1, the slot replaced.
func TestRRCov_32_refusesEverythingWithACode(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "x.example", true)
	f.nodes["a"].dead.Store(true)
	ctx := f.keyOn(t, "x.example", 0)
	if _, answered := f.dial(t, ctx, "x.example"); !answered {
		t.Fatal("the retry answers")
	}
	if shift, _ := f.shift(ctx, "x.example"); shift != 1 {
		t.Fatalf("K1: shifted at once, got %d", shift)
	}
	f.waitCheck(t)
	if f.allHistory("a") || !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("D1 after the failed life probe: %v", f.pool())
	}
}

// --- K2 ------------------------------------------------------------------------------

// K2 premises one by one, on signs given straight to the rule: a parallel
// connection of the key is the same attempt; an ANSWER to the key that started at
// or after a1 — also one that came before b1 — voids the record; one begun before
// a1 does not; a proof via the node between the starts is needed.
func TestRRCov_K2_premises(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	b := f.s.balancer
	track := b.trackOf("a")
	second := int64(time.Second)
	base := rrClock(time.Now())
	keys := 0
	newKey := func() (rrKey, context.Context) {
		keys++
		host := fmt.Sprintf("k%d.example", keys)
		ctx := f.keyOn(t, host, 0)
		return b.rrKeyOf(ctx, siteAddr(host)), ctx
	}
	shifted := func(key rrKey) bool { return b.memory.shifts.shiftOf(key.table) != 0 }
	sign := func(key rrKey, a, at int64) { f.s.rrSoftSign(key, 0, "a", a, at, false, true) }

	key, _ := newKey()
	sign(key, base, base+10*second)
	sign(key, base+5*second, base+20*second) // started before the first outcome
	if shifted(key) {
		t.Fatal("a parallel connection is the same attempt")
	}
	track.addProof(base+second, base+2*second)
	sign(key, base+11*second, base+12*second)
	if !shifted(key) {
		t.Fatal("the second attempt after an ANSWER via a between the starts shifts")
	}

	base += 100 * second
	key, _ = newKey()
	track.keyAnswered(key.table, base+second) // an ANSWER to the key that came before b1
	sign(key, base, base+10*second)
	track.addProof(base+2*second, base+3*second)
	sign(key, base+11*second, base+12*second)
	if shifted(key) {
		t.Fatal("an ANSWER to the key since a1 that came before b1: no shift")
	}

	base += 100 * second
	key, _ = newKey()
	sign(key, base, base+10*second)
	track.addProof(base+2*second, base+3*second)
	track.keyAnswered(key.table, base+4*second) // after b1: the proof is that ANSWER
	sign(key, base+11*second, base+12*second)
	if shifted(key) {
		t.Fatal("the proof between the starts was the key's own ANSWER: no shift")
	}

	base += 100 * second
	key, _ = newKey()
	track.keyAnswered(key.table, base-second) // begun before a1
	sign(key, base, base+10*second)
	sign(key, base+11*second, base+12*second)
	if shifted(key) {
		t.Fatal("no ANSWER via a between the starts: no shift")
	}
	track.addProof(base+12*second, base+13*second)
	sign(key, base+14*second, base+15*second)
	if !shifted(key) {
		t.Fatal("an ANSWER to the key begun before a1 does not void the record")
	}
}

// I7: a record pushed out of the full table gives no shift; D1 and Q2 erase the
// records of the node.
func TestRRCov_K2_evictionAndErasure(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	b := f.s.balancer
	track := b.trackOf("a")
	second := int64(time.Second)
	base := rrClock(time.Now())
	key := rrKey{valid: true, table: 1}
	f.s.rrSoftSign(key, 0, "a", base, base+second, false, true)
	for i := 0; i < rrK2Size; i++ {
		f.s.rrSoftSign(rrKey{valid: true, table: uint64(1000 + i)}, 0, "a", base, base+second, false, true)
	}
	track.addProof(base+second, base+2*second)
	f.s.rrSoftSign(key, 0, "a", base+3*second, base+4*second, false, true)
	if b.memory.shifts.shiftOf(key.table) != 0 {
		t.Fatal("the record was pushed out: no shift")
	}
	b.memory.k2.forget("a") // D1, Q2
	if b.memory.k2.size.Load() != 0 {
		t.Fatal("the records of the node erased")
	}
	f.s.rrSoftSign(key, 0, "a", base+5*second, base+6*second, false, true)
	if b.memory.shifts.shiftOf(key.table) != 0 {
		t.Fatal("after the erasure the sign is a first one again")
	}

	g := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
	now := rrClock(time.Now())
	g.s.rrSoftSign(rrKey{valid: true, table: 3}, 0, "a", now, now, false, true)
	g.s.balancer.rrCountConn("a", time.Now())
	g.nodes["a"].dead.Store(true)
	g.nodes["c"].dead.Store(true) // no replacement in this check
	g.runCheck()
	if g.s.balancer.memory.k2.size.Load() != 0 || g.allHistory("a") || !samePool(g.pool(), "", "b") {
		t.Fatal("D1 erases the K2 records of the struck node; its slot is empty (R1)")
	}
	if conns, _ := countsOf(g.s.balancer, "a"); conns != 0 {
		t.Fatal("Q3: D1 drops the counters of the node")
	}
	g.s.balancer.rrCountConn("b", time.Now())
	g.s.balancer.replaceSlot("b", "c")
	if conns, _ := countsOf(g.s.balancer, "b"); conns != 0 {
		t.Fatal("Q3: a node that left its slot drops its counters")
	}
}

// K2, §2: the shift K2 made goes back when a SILENT it rested on gets its ANSWER,
// or when an ANSWER to the key since a1 came in while it was made; a shift that
// rested on no SILENT, a later shift and a K1 shift stay.
func TestRRCov_K2_shiftBack(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	key := f.s.balancer.rrKeyOf(f.keyOn(t, "back.example", 0), siteAddr("back.example"))
	shiftOf := func() int { return f.s.balancer.memory.shifts.shiftOf(key.table) }

	f.s.rrShiftOn(key, 0, "a", rrShiftK2Silent)
	f.s.rrShiftBack(key, 0, true)
	if shiftOf() != 0 {
		t.Fatal("a SILENT it rested on got its ANSWER: back")
	}
	f.s.rrShiftOn(key, 0, "a", rrShiftK2)
	f.s.rrShiftBack(key, 0, true)
	if shiftOf() != 1 {
		t.Fatal("no SILENT under it: a SILENT's ANSWER does not take it back")
	}
	f.s.rrShiftBack(key, 0, false)
	if shiftOf() != 0 {
		t.Fatal("an ANSWER to the key while it was made: back")
	}
	f.s.rrShiftOn(key, 0, "a", rrShiftK2Silent)
	f.s.rrShiftOn(key, 1, "b", rrShiftK1)
	f.s.rrShiftBack(key, 0, true)
	f.s.rrShiftBack(key, 1, false)
	if shiftOf() != 2 {
		t.Fatal("a later shift, a K1 shift: they stay")
	}
}

// §2, Q1: the count of a SILENT and its ANSWER race; whichever comes second takes
// the count back, once.
func TestRRCov_silentCountSettlesOnce(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, answerFirst := range []bool{true, false} {
		key := rrKey{valid: true, table: 42}
		if !f.s.rrCreditFail("a", key) {
			t.Fatal("precondition: counted")
		}
		c := &rrFirstByteConn{}
		c.watch = &rrConnWatch{s: f.s, member: "a", key: key}
		c.set(rrStateSilent)
		if answerFirst {
			c.set(rrStateGotByte)
			c.settle()
			c.set(rrStateCounted)
			c.settle()
		} else {
			c.set(rrStateCounted)
			c.settle()
			c.set(rrStateGotByte)
			c.settle()
		}
		c.settle()
		if _, fails := countsOf(f.s.balancer, "a"); fails != 0 {
			t.Fatalf("answer first %v: the count taken back once, fails %d", answerFirst, fails)
		}
	}
	c := &rrFirstByteConn{}
	c.set(rrStateGotByte)
	if c.setUnless(rrStateSilent, rrStateEnded) {
		t.Fatal("no SILENT after the first byte")
	}
}

// §2: an interrupt of the group closes the waiting connection from our side —
// CANCEL: no Q1 sign, no K2 record. A close by the destination is a sign.
func TestRRCov_interruptGivesNoSign(t *testing.T) {
	for _, interrupt := range []bool{true, false} {
		f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
		host := "wait.example"
		if interrupt {
			f.fb["a"].set(&f.fb["a"].mute, host, true)
		} else {
			f.fb["a"].set(&f.fb["a"].silent, host, true)
		}
		conn, err := f.s.DialContext(f.keyOn(t, host, 0), N.NetworkTCP, siteAddr(host))
		if err != nil {
			t.Fatal(err)
		}
		conn.Write([]byte("hi"))
		done := make(chan struct{})
		go func() {
			conn.Read(make([]byte, 8))
			close(done)
		}()
		if interrupt {
			time.Sleep(20 * time.Millisecond)
			f.g.interruptGroup.Interrupt(true)
		}
		waitClosed(t, done)
		conn.Close()
		sign := f.s.balancer.trackOf("a").pendingN.Load() != 0 || f.s.balancer.memory.k2.size.Load() != 0
		if sign == interrupt {
			t.Fatalf("interrupt %v: a sign %v", interrupt, sign)
		}
	}
}

// A read that ran into the consumer's deadline is no outcome for TCP: no sign, and
// the connection stays watched — its later first byte is the ANSWER.
func TestRRCov_readTimeoutIsNoOutcome(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn, f := watched(t, client)
	conn.watch.track = f.s.balancer.trackOf("a")
	release := make(chan struct{})
	go func() {
		server.Read(make([]byte, 8))
		<-release
		server.Write([]byte("ok"))
	}()
	conn.Write([]byte("hi"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	buffer := make([]byte, 8)
	if n, err := conn.Read(buffer); n != 0 || !E.IsTimeout(err) {
		t.Fatalf("precondition: a read timeout, got %d %v", n, err)
	}
	if conn.state.Load()&rrStateEnded != 0 || conn.watch.track.pendingN.Load() != 0 || conn.watch.track.waits.oldest.Load() == 0 {
		t.Fatal("a read timeout is no outcome: the connection still waits")
	}
	conn.SetReadDeadline(time.Time{})
	close(release)
	if n, _ := conn.Read(buffer); n == 0 {
		t.Fatal("precondition: the answer")
	}
	if !provedAny(f.s.balancer) {
		t.Fatal("its first byte is the ANSWER")
	}
}

// A probe of the scheduled run that answered proves the path via the node; not in
// a freeze.
func TestRRCov_runAnswerIsAProof(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	f.s.balancer.heal.freeze()
	f.s.rrRunAnswer("a")
	if provedAny(f.s.balancer) {
		t.Fatal("N0: nothing in a freeze")
	}
	f.s.balancer.heal.epochEnd.Store(rrClock(time.Now()) - int64(rrProbeTimeout()))
	f.s.rrRunAnswer("a")
	if f.s.balancer.trackOf("a").proved.Load() == math.MinInt64 {
		t.Fatal("the answer of the run proves the path via a")
	}
}

// The slot hash built without strings is the hash of the sticky key: the slots of
// the keys do not move. The table key and the log label as well.
func TestRRCov_stickyHashIsHashOfStickyKey(t *testing.T) {
	b := &balancer{stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickySourceIP, C.URLTestStickyDomain, C.URLTestStickyDestIP, C.URLTestStickyDestPort}}
	metadata := &adapter.InboundContext{
		ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/usr/bin/app"},
		Source:      M.ParseSocksaddrHostPort("2001:db8::1", 5000),
		Domain:      "example.com",
	}
	ctx := adapter.WithContext(context.Background(), metadata)
	for _, destination := range []M.Socksaddr{
		M.ParseSocksaddrHostPort("203.0.113.7", 443),
		M.ParseSocksaddrHostPort("2001:db8::2", 8443),
		M.ParseSocksaddrHostPort("example.com", 80),
		{},
	} {
		if got, want := b.rrKeyOf(ctx, destination).sticky, hashKey(b.stickyKey(ctx, destination)); got != want {
			t.Fatalf("%s: sticky hash %x, want %x", destination, got, want)
		}
		if got, want := b.rrKeyOf(context.Background(), destination).sticky, hashKey(b.stickyKey(context.Background(), destination)); got != want {
			t.Fatalf("%s without metadata: sticky hash %x, want %x", destination, got, want)
		}
	}
	process := &balancer{stickyHash: []string{C.URLTestStickyProcess}}
	destination := M.ParseSocksaddrHostPort("203.0.113.7", 443)
	key := process.rrKeyOf(ctx, destination)
	want := fnvString(fnvString(hashKey(process.stickyKey(ctx, destination)), "\x00\x00"), "example.com")
	if key.table != want || key.label(process.stickyHash) != "example.com" {
		t.Fatalf("domain table key %x, want %x", key.table, want)
	}
	key = process.rrKeyOf(context.Background(), destination)
	want = fnvString(fnvString(hashKey(process.stickyKey(context.Background(), destination)), "\x00\x00"), "203.0.113.7")
	if key.table != want || key.label(process.stickyHash) != "203.0.113.7" {
		t.Fatalf("IP table key %x, want %x", key.table, want)
	}
	ported := &balancer{stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickyDestPort}}
	if label := ported.rrKeyOf(context.Background(), destination).label(ported.stickyHash); label != "203.0.113.7:443" {
		t.Fatalf("an IP label with the port in the key: got %q", label)
	}
}

// --- §2: the classes of a failed open by platform ------------------------------------

func TestRRCov_outcomeClassesByPlatform(t *testing.T) {
	opError := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	gvisor := func(text string) error {
		return &net.OpError{Op: "connect", Net: "tcp", Err: E.New(text)}
	}
	local := []error{
		opError(syscall.ENETUNREACH), opError(syscall.EHOSTUNREACH), opError(syscall.ENETDOWN), opError(syscall.EHOSTDOWN), opError(syscall.EADDRNOTAVAIL),
		opError(syscall.Errno(10051)), opError(syscall.Errno(10065)), opError(syscall.Errno(10050)), opError(syscall.Errno(10064)), opError(syscall.Errno(10049)),
		E.Cause(tun.ErrNoRoute, "bind"), E.Extend(tun.ErrNoRoute, "no route in default table 254"),
		E.Cause(E.New("no available network interface"), "dial tcp"),
		gvisor("network is unreachable"), gvisor("no route to host"), gvisor("host is down"), gvisor("machine is not on the network"), gvisor("bad local address"),
		E.Errors(E.New("dial a"), opError(syscall.Errno(10051))),
	}
	for _, err := range local {
		if !rrLocalError(err) || rrActiveRefusal(err) {
			t.Fatalf("LOCAL: %v", err)
		}
	}
	refusal := []error{
		opError(syscall.ECONNREFUSED), opError(syscall.ECONNRESET), opError(syscall.Errno(10061)), opError(syscall.Errno(10054)),
		gvisor("connection was refused"), gvisor("connection reset by peer"),
	}
	for _, err := range refusal {
		if !rrActiveRefusal(err) || rrLocalError(err) {
			t.Fatalf("an active refusal: %v", err)
		}
	}
	for _, err := range []error{opError(syscall.ETIMEDOUT), opError(syscall.Errno(10060)), gvisor("operation timed out"), context.DeadlineExceeded} {
		if !rrSilence(err) {
			t.Fatalf("a timeout: %v", err)
		}
	}
	for _, err := range []error{
		E.New("socks5: network is unreachable"), E.New("network is unreachable through the proxy"), gvisor("connection aborted"), E.New("EOF"),
	} {
		if rrLocalError(err) || rrActiveRefusal(err) || rrSilence(err) {
			t.Fatalf("not a class of its own: %v", err)
		}
	}

	site := &destinationNode{tag: "m"}
	proxy := &cycleNode{tag: "p"}
	manager := &fakeManager{byTag: map[string]adapter.Outbound{"m": site, "p": proxy}}
	s := &URLTest{outbound: manager, group: &URLTestGroup{ctx: context.Background(), outbound: manager}, lxURLTest: lxURLTest{balancer: &balancer{}}}
	if s.rrOutcome(site, opError(syscall.Errno(10061))) != rrRefused || s.rrOutcome(site, gvisor("connection was refused")) != rrRefused {
		t.Fatal("a refusal of a leaf that goes to the site itself: explicit REFUSE_DST, also on Windows and from gVisor")
	}
	if s.rrOutcome(proxy, opError(syscall.Errno(10061))) != rrFailActive || s.rrOutcome(site, gvisor("no route to host")) != rrLocal ||
		s.rrOutcome(proxy, gvisor("operation timed out")) != rrFailTimeout {
		t.Fatal("a proxy's refusal is FAIL_NODE; LOCAL and the timeout by text")
	}
}

// lx:end SPEC 138
