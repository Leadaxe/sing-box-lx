// lx:begin SPEC 138 round_robin self-heal: the scenarios of ALGORITHM.md §7

package group

import (
	"context"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
)

// shortProbes sets T for the test.
func shortProbes(t *testing.T, timeout time.Duration) {
	old := rrProbeTimeoutNs.Load()
	rrProbeTimeoutNs.Store(int64(timeout))
	t.Cleanup(func() { rrProbeTimeoutNs.Store(old) })
}

// runCheck runs one check of the pool to its end and clears its pause.
func (f *healFixture) runCheck() {
	f.s.balancer.heal.holdRound()
	f.s.rrHealCheck()
	f.s.balancer.heal.clearPause()
}

// runSearch runs one replacement search to its end and clears its pause.
func (f *healFixture) runSearch() {
	f.s.balancer.heal.holdRound()
	f.s.rrHealSearch()
	f.s.balancer.heal.clearPause()
}

func (f *healFixture) allHistory(tags ...string) bool {
	for _, tag := range tags {
		if f.g.history.LoadURLTestHistory(tag) == nil {
			return false
		}
	}
	return true
}

// С1, С2, scenario 4: the network goes down with no platform signal — every probe
// and every open fails — and comes back. Retries go on; no strike, no shift, no
// count, no freeze (I1).
func TestRRAlgo_C1_networkDownAndBack(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, node := range f.fb {
		node.dead.Store(true)
	}
	key := f.keyOn(t, "site.example", 0)
	for i := 0; i < 3; i++ {
		if opened, _ := f.dial(t, key, "site.example"); opened {
			t.Fatal("precondition: the network is down")
		}
		f.waitCheck(t)
	}
	heal := &f.s.balancer.heal
	if !f.allHistory("a", "b", "c") || !samePool(f.pool(), "a", "b", "c") || f.fails("a")+f.fails("b") != 0 ||
		f.s.balancer.memory.shifts.size.Load() != 0 || heal.epochEnd.Load() != 0 {
		t.Fatal("I1: no strike, no shift, no count, no event while the network is down")
	}
	for _, node := range f.fb {
		node.dead.Store(false)
	}
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("back: the connection answers")
	}
	f.waitCheck(t)
	if !f.allHistory("a", "b", "c") || f.fails("a") != 0 || f.s.balancer.memory.shifts.size.Load() != 0 || heal.frozen() {
		t.Fatal("nothing is left behind by the outage")
	}
}

// Scenario 1, 33, V6: a killed node. The open fails at once → retry, the life probe
// (S1) has no ANSWER → the check: the decisive probe and the control after → D1;
// the slot takes the first candidate that answered its probe — d, alive on the last
// run but dead now, never takes it by its history.
func TestRRAlgo_1_killedNodeStruckAndReplaced(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].dead.Store(true)
	f.nodes["d"].dead.Store(true)
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if !ok {
		t.Fatal("the retry answers")
	}
	f.waitCheck(t)
	waitUntil(t, func() bool { return samePool(f.pool(), "e", "b", "c") })
	if f.g.history.LoadURLTestHistory("a") != nil || f.probes("d") == 0 {
		t.Fatal("a struck out; d probed and passed over")
	}
}

// S1: a fast refusal sets no bypass until the life probe ended without ANSWER.
func TestRRAlgo_S1_noBypassBeforeTheLifeProbeFails(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	hold := make(chan struct{})
	f.nodes["a"].hold = hold
	f.nodes["a"].dead.Store(true)
	heal := &f.s.balancer.heal
	heal.holdRound() // the check is not under test
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return f.probes("a") == 1 })
	if track.suspect(rrClock(time.Now()), heal.epoch()) {
		t.Fatal("S1: no bypass while the life probe runs")
	}
	close(hold)
	waitUntil(t, func() bool { return track.suspect(rrClock(time.Now()), heal.epoch()) })
	if !track.wantCheck.Load() {
		t.Fatal("the failed life probe asks for the check")
	}
	heal.dropRound()
	f.waitCheck(t)
}

// Scenario 3: a hung node (accepts, stays silent). A look finds the open older than
// H: the bypass and the life probe at once (S2); the probe is silent → the check:
// the decisive probe after the control is silent, the control after answers → D1;
// the hung open is cancelled and goes to the retry.
func TestRRAlgo_3_hungNodeBypassedAndStruck(t *testing.T) {
	shortProbes(t, 300*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	never := make(chan struct{})
	defer close(never)
	f.fb["a"].hold = never
	f.fb["a"].set(&f.fb["a"].stall, "site.example", true)
	key := f.keyOn(t, "site.example", 0)
	track := f.s.balancer.trackOf("a")
	done := f.dialAsync(key, "site.example")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	ageTrack(track, rrHangAfter+time.Second)
	if node, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example")); node.Tag() == "a" {
		t.Fatal("S2: the slot is bypassed from the look on")
	}
	result := waitDial(t, done)
	if result.err != nil {
		t.Fatal("the cancelled open goes to the retry: ", result.err)
	}
	result.conn.Close()
	f.waitCheck(t)
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	if f.probes("a") != 3 {
		t.Fatalf("the life probe, the first and the decisive probe: %d", f.probes("a"))
	}
}

// A live node whose first probe of the check fails once (an EOF): its decisive probe
// answers — D3, it stays (V7).
func TestRRAlgo_liveNodeWithOneEOFIsNotStruck(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].probeFails.Store(1)
	f.runCheck()
	if !samePool(f.pool(), "a", "b", "c") || f.g.history.LoadURLTestHistory("a") == nil || f.probes("a") != 2 {
		t.Fatalf("a answered its decisive probe and stays: pool %v, probes %d", f.pool(), f.probes("a"))
	}
}

// Scenario 10, K4, K2: a site closed without a byte through a. The first close is a
// record only, a second one without an ANSWER via a in between shifts nothing
// (I1′); after a answered another key, the next close shifts the key. Q1 counts
// the key once.
func TestRRAlgo_10_secondSoftSignShifts(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "geo.example", true)
	key := f.keyOn(t, "geo.example", 0)
	for i := 0; i < 2; i++ {
		if _, answered := f.dial(t, key, "geo.example"); answered {
			t.Fatal("precondition: closed without a byte")
		}
		if shift, _ := f.shift(key, "geo.example"); shift != 0 {
			t.Fatalf("close %d with no ANSWER via a in between: no shift, got %d", i+1, shift)
		}
	}
	if _, answered := f.dial(t, f.keyOn(t, "ok.example", 0), "ok.example"); !answered {
		t.Fatal("precondition: a answers another key")
	}
	f.dial(t, key, "geo.example")
	if shift, _ := f.shift(key, "geo.example"); shift != 1 {
		t.Fatalf("K2: the second close after an ANSWER via a shifts the key, got %d", shift)
	}
	if f.fails("a") != 1 {
		t.Fatalf("Q1: once per key, got %d", f.fails("a"))
	}
	if opened, answered := f.dial(t, key, "geo.example"); !opened || !answered || f.dials("b") == 0 {
		t.Fatal("the key goes through b now")
	}
}

// Scenario 9, K1: an explicit refusal shifts the key at once and starts the life
// probe of the node; the node is counted once the retry of the same connection
// answered (V4).
func TestRRAlgo_9_explicitRefusalShiftsAtOnce(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	if _, answered := f.dial(t, key, "blocked.example"); !answered {
		t.Fatal("the retry through b answers")
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("K1: shifted at once, got %d", shift)
	}
	f.waitCheck(t)
	if f.probes("a") != 1 || f.fails("a") != 1 || f.g.history.LoadURLTestHistory("a") == nil {
		t.Fatalf("the life probe answered, one count: probes %d fails %d", f.probes("a"), f.fails("a"))
	}
}

// Scenario 26 and §2 LOCAL: only the retry; the life probe only for a proxy leaf;
// no shift, no count, no K2 record.
func TestRRAlgo_26_localOnlyRetries(t *testing.T) {
	local := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fb["a"].dialErr = local
	key := f.keyOn(t, "site.example", 0)
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("the retry answers")
	}
	f.waitCheck(t)
	if f.probes("a") != 1 || f.fails("a") != 0 || f.s.balancer.memory.shifts.size.Load() != 0 || f.s.balancer.memory.k2.size.Load() != 0 {
		t.Fatal("LOCAL of a proxy leaf: the life probe and nothing else")
	}
	d := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	d.nodes["a"].direct = true
	destination := siteAddr("site.example")
	conn, ok := d.s.rrHealDial(context.Background(), N.NetworkTCP, destination, d.nodes["a"], local, d.s.testPick(context.Background(), destination, time.Now()))
	closeConn(conn)
	d.waitCheck(t)
	if !ok || d.probes("a") != 0 {
		t.Fatal("LOCAL of a direct leaf: the retry, no probe")
	}
}

// K5 (V2): |P| shifts in a row with no ANSWER to the key mark it; an ANSWER restarts
// the circle. A marked key keeps the retry of FAIL_NODE, not of an explicit refusal.
func TestRRAlgo_K5_circleAndAnswer(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "x.example", 0)
	key := f.s.balancer.rrKeyOf(ctx, siteAddr("x.example"))
	f.shiftOn(key, 0, "a")
	f.shiftOn(key, 1, "b")
	f.s.balancer.memory.shifts.answered(key.table)
	f.shiftOn(key, 2, "c")
	f.shiftOn(key, 3, "a")
	if _, marked := f.shift(ctx, "x.example"); marked {
		t.Fatal("an ANSWER to the key restarts the circle")
	}
	f.shiftOn(key, 4, "b")
	if shift, marked := f.shift(ctx, "x.example"); !marked || shift != 0 {
		t.Fatal("three shifts in a row: marked, back to its own slot")
	}
	f.fb["a"].siteDown.Store(true)
	if _, answered := f.dial(t, ctx, "x.example"); !answered {
		t.Fatal("a marked key keeps the retry of FAIL_NODE")
	}
	f.waitCheck(t)
	f.fb["a"].siteDown.Store(false)
	f.fb["a"].set(&f.fb["a"].openFail, "x.example", true)
	if opened, _ := f.dial(t, ctx, "x.example"); opened {
		t.Fatal("a marked key gets no retry of an explicit refusal")
	}
	f.waitCheck(t)
}

// watchedOn opens a watched connection through the pool member tag over a pipe and
// sends the client's first bytes; the far end is returned.
func (f *shiftFixture) watchedOn(t *testing.T, ctx context.Context, host string, tag string) (*rrFirstByteConn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	key := f.s.balancer.rrKeyOf(ctx, siteAddr(host))
	picked := rrPick{key: key, shift: f.s.balancer.memory.shifts.shiftOf(key.table), start: time.Now(), track: f.s.balancer.trackOf(tag)}
	conn := f.s.rrWatch(ctx, N.NetworkTCP, f.fb[tag], client, picked, nil, false).(*rrFirstByteConn)
	go func() {
		buffer := make([]byte, 8)
		server.Read(buffer)
	}()
	conn.Write([]byte("hi"))
	t.Cleanup(func() { conn.Close(); server.Close() })
	return conn, server
}

// age moves the wait of c d into the past.
func age(c *rrFirstByteConn, d time.Duration) {
	c.wroteAt.Add(-int64(d))
	ageTrack(c.watch.track, d)
}

// Q1 for an implicit REFUSE_DST: a close shorter than H counts once an ANSWER via
// the node comes (scenario 31); a longer one only with Live(X; {n}) — the rule of
// the half.
func TestRRAlgo_31_Q1closeWithoutCode(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	track := f.s.balancer.trackOf("a")
	short, server := f.watchedOn(t, f.keyOn(t, "one.example", 0), "one.example", "a")
	server.Close()
	short.Read(make([]byte, 8))
	if track.pendingN.Load() != 1 {
		t.Fatal("a close shorter than H waits for an ANSWER via a")
	}
	now := rrClock(time.Now())
	f.s.rrProof(track, now, now)
	if f.fails("a") != 1 {
		t.Fatal("the ANSWER after it counts it")
	}

	long, server := f.watchedOn(t, f.keyOn(t, "two.example", 0), "two.example", "a")
	age(long, rrHangAfter+time.Second)
	server.Close()
	long.Read(make([]byte, 8))
	if track.pendingN.Load() != 0 {
		t.Fatal("a close later than H with no Live: no sign")
	}

	proven, server := f.watchedOn(t, f.keyOn(t, "three.example", 0), "three.example", "a")
	age(proven, rrHangAfter+time.Second)
	w := proven.wroteAt.Load()
	track.addProof(w+1, w+int64(time.Second)) // the path confirmed in the first half
	server.Close()
	proven.Read(make([]byte, 8))
	if track.pendingN.Load() != 1 {
		t.Fatal("a close later than H with Live: a sign")
	}
}

// Scenarios 12, 29, С6: SILENT at a look after T with the path confirmed in the
// first half counts at once (V1); a second SILENT of the key with an ANSWER via the
// node between shifts the key (K2); the late ANSWER of the first one takes the count
// and the shift back.
func TestRRAlgo_12_silentCountsShiftsAndIsTakenBack(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "slow.example", 0)
	track := f.s.balancer.trackOf("a")
	silentFor := rrProbeTimeout() + 2*time.Second

	x1, server1 := f.watchedOn(t, ctx, "slow.example", "a")
	age(x1, silentFor)
	w1 := x1.wroteAt.Load()
	track.addProof(w1+1, w1+int64(time.Second))
	f.s.rrInspect(track, rrClock(time.Now()))
	if x1.state.Load()&rrStateSilent == 0 || f.fails("a") != 1 {
		t.Fatal("SILENT counts at the look")
	}
	if shift, _ := f.shift(ctx, "slow.example"); shift != 0 {
		t.Fatal("K4: the first SILENT is a record only")
	}

	time.Sleep(time.Millisecond)
	x2, _ := f.watchedOn(t, ctx, "slow.example", "a")
	track.addProof(rrClock(x1.watch.start)+1, rrClock(x2.watch.start)-1) // an ANSWER via a between the starts
	age(x2, silentFor)
	w2 := x2.wroteAt.Load()
	track.addProof(w2+1, w2+int64(time.Second))
	f.s.rrInspect(track, rrClock(time.Now()))
	if shift, _ := f.shift(ctx, "slow.example"); shift != 1 || f.fails("a") != 1 {
		t.Fatalf("K2: shifted by the second SILENT, counted once per key: shift %d", shift)
	}

	go server1.Write([]byte("ok"))
	if n, _ := x1.Read(make([]byte, 8)); n == 0 {
		t.Fatal("precondition: the late answer")
	}
	if shift, _ := f.shift(ctx, "slow.example"); shift != 0 || f.fails("a") != 0 {
		t.Fatalf("the ANSWER takes the count and the shift back: shift %d fails %d", shift, f.fails("a"))
	}
}

// D5: no probe and no candidate answered — no verdict, no event, the gap set.
func TestRRAlgo_D5_noControlNoVerdict(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	for _, node := range f.nodes {
		node.dead.Store(true)
	}
	f.s.balancer.heal.holdRound()
	f.s.rrHealCheck()
	if !f.allHistory("a", "b", "c") || !samePool(f.pool(), "a", "b", "c") || f.s.balancer.heal.epochEnd.Load() != 0 || f.s.balancer.heal.pauseEnd() == nil {
		t.Fatal("D5: nothing changes, no event, the gap holds")
	}
	if total := f.probes("a") + f.probes("b") + f.probes("c") + f.probes("d"); total != 4 {
		t.Fatalf("the pool and one batch: %d probes", total)
	}
}

// Scenario 19, L3: a pool of one, dead, the candidates dead from the cursor on. A
// check probes at most k batches; the cursor goes on between checks; the third
// check finds the live candidate, the control, and strikes the node out.
func TestRRAlgo_19_cursorAndK(t *testing.T) {
	tags := []string{"a", "c1", "c2", "c3", "c4", "c5", "c6"}
	f := newHealFixture(t, tags, []string{"a"}, 0)
	for _, tag := range tags[:6] {
		f.nodes[tag].dead.Store(true)
	}
	for _, tag := range tags[1:] {
		f.g.history.DeleteURLTestHistory(tag)
	}
	f.runCheck()
	if f.probes("c1") != 1 || f.probes("c2") != 1 || f.probes("c3") != 0 || !samePool(f.pool(), "a") {
		t.Fatal("check 1: two batches, no verdict")
	}
	f.runCheck()
	if f.probes("c3") != 1 || f.probes("c4") != 1 || f.probes("c5") != 0 || f.probes("c1") != 1 {
		t.Fatal("check 2: the cursor goes on")
	}
	f.runCheck()
	if !samePool(f.pool(), "c6") || f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatalf("check 3: c6 is the control and takes the slot, pool %v", f.pool())
	}
}

// I9, R1: an empty slot (D1), like a slot whose node has no history (§5), starts the
// replacement search only — no probe of a pool node, no control — and the
// candidate that answered its own probe fills it.
func TestRRAlgo_I9_emptySlotStartsOnlyTheSearch(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].dead.Store(true)
	f.nodes["e"].dead.Store(true) // d alone answers the first search
	f.g.history.DeleteURLTestHistory("a")
	f.s.selectBalanced(context.Background(), N.NetworkTCP, siteAddr("site.example"))
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	f.waitCheck(t)
	if f.probes("a")+f.probes("b")+f.probes("c") != 0 || f.probes("d") != 1 {
		t.Fatalf("the slot without history: the search probes candidates only: a %d, b %d, c %d, d %d", f.probes("a"), f.probes("b"), f.probes("c"), f.probes("d"))
	}
	f.nodes["e"].dead.Store(false)
	f.nodes["b"].dead.Store(true) // what D1 does to b
	f.g.history.DeleteURLTestHistory("b")
	f.s.balancer.clearSlot("b")
	f.s.balancer.heal.clearPause() // the pause is over
	f.s.balancer.poolLook.Store(0)
	c, d, e := f.probes("c"), f.probes("d"), f.probes("e")
	f.s.selectBalanced(context.Background(), N.NetworkTCP, siteAddr("site.example"))
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "e", "c") })
	f.waitCheck(t)
	if f.probes("c") != c || f.probes("d") != d || f.probes("e") != e+1 {
		t.Fatalf("the empty slot: the search probes candidates only: c %d, d %d, e %d", f.probes("c")-c, f.probes("d")-d, f.probes("e")-e)
	}
}

// V9: a UDP flow with no answer starts the life probe of its node with no bypass for
// TCP; at most one such probe per group every rrUDPLifeGap.
func TestRRAlgo_V9_udpWaitNoTCPBypass(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.udp["a"].mute.Store(true)
	conn, err := f.listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.WriteTo([]byte("hi"), nil)
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return track.pwaits.oldest.Load() != 0 })
	ageTrack(track, rrHangAfter+time.Second)
	f.nodes["a"].dead.Store(true)
	heal := &f.s.balancer.heal
	heal.holdRound() // the check is not under test
	f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
	waitUntil(t, func() bool { return f.probes("a") == 1 && !track.lifeBusy.Load() })
	if track.suspect(rrClock(time.Now()), heal.epoch()) || !track.wantCheck.Load() {
		t.Fatal("no bypass for TCP; the failure asks for the check")
	}
	if f.s.rrLifeProbe(f.udp["b"], f.s.balancer.trackOf("b"), 0, true) {
		t.Fatal("one UDP life probe per group per minute")
	}
	heal.dropRound()
}

// S2 at a failed open by a timeout: the bypass at once; Q1 records the sign only
// with Live(X; P \ {n}).
func TestRRAlgo_S2_openTimeout(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.s.balancer.heal.holdRound()
	defer f.s.balancer.heal.dropRound()
	// The probe the failed open starts through a gets no answer while the rule is read.
	f.nodes["a"].hold = make(chan struct{})
	defer close(f.nodes["a"].hold)
	destination := siteAddr("site.example")
	start := time.Now().Add(-6 * time.Second)
	timeout := os.ErrDeadlineExceeded
	conn, _ := f.s.rrHealDial(context.Background(), N.NetworkTCP, destination, f.nodes["a"], timeout, f.s.testPick(context.Background(), destination, start))
	closeConn(conn)
	track := f.s.balancer.trackOf("a")
	if !track.suspect(rrClock(time.Now()), f.s.balancer.heal.epoch()) || track.pendingN.Load() != 0 {
		t.Fatal("S2: the bypass at once; no Live, no Q1 sign")
	}
	w := rrClock(start)
	f.s.balancer.trackOf("b").addProof(w+1, w+int64(time.Second))
	conn, _ = f.s.rrHealDial(context.Background(), N.NetworkTCP, destination, f.nodes["a"], timeout, f.s.testPick(context.Background(), destination, start))
	closeConn(conn)
	if track.pendingN.Load() != 1 {
		t.Fatal("Live(X; P \\ {n}) through b: the Q1 sign")
	}
}

// The rule of the half and the front of the proofs.
func TestRRAlgo_proofsAndTheHalf(t *testing.T) {
	track := newRRTrack("a")
	second := int64(time.Second)
	w := int64(100 * second)
	// b − b' = (b' − w) + ρ exactly: Live holds.
	b := w + 3*second
	bound := rrHalf(w, b)
	track.addProof(w, bound)
	if !track.live(w, rrHalf(w, b)) || track.live(w, rrHalf(w, b-1)) || track.live(w+1, rrHalf(w, b)) {
		t.Fatal("the boundary of the rule of the half")
	}
	// A proof with a later start and an earlier answer replaces the one it covers.
	track.addProof(w+second/2, bound-1)
	if track.proofN != 1 || !track.live(w, bound-1) || track.live(w+second/2+1, bound) {
		t.Fatal("the front keeps the stronger proof")
	}
	// Answers closer than the grain keep the later one; the front stays bounded.
	for i := int64(0); i < 10*rrProofMax; i++ {
		track.addProof(w+2*second+i*int64(rrProofGrain)/3, w+2*second+i*int64(rrProofGrain)/3+1)
	}
	if track.proofN > rrProofMax || track.live(w+1000*second, w+1000*second) {
		t.Fatal("bounded, and no proof claimed that was not seen")
	}
}

type destinationNode struct {
	adapter.Outbound
	tag string
}

func (n *destinationNode) Type() string             { return C.TypeMASQUE }
func (n *destinationNode) Tag() string              { return n.tag }
func (n *destinationNode) DialsDestinationLx() bool { return true }
func (n *destinationNode) Network() []string        { return []string{N.NetworkTCP} }

// An outbound that dials the destination itself: its refused open is the site's
// answer for SPEC 054 (no dead path) and an explicit REFUSE_DST for SPEC 138.
func TestSiteOnOpen_destinationDialer(t *testing.T) {
	node := &destinationNode{tag: "m"}
	manager := &fakeManager{byTag: map[string]adapter.Outbound{"m": node}}
	if !siteOnOpenLx(context.Background(), manager, node) {
		t.Fatal("an outbound that dials the destination itself goes to the site on open")
	}
	g := &URLTestGroup{ctx: context.Background(), outbound: manager}
	if g.pathDead(node, syscall.ECONNREFUSED) {
		t.Fatal("SPEC 054: its refused open is no dead path")
	}
	s := &URLTest{outbound: manager, group: g, lxURLTest: lxURLTest{balancer: &balancer{}}}
	if s.rrOutcome(node, syscall.ECONNREFUSED) != rrRefused || s.rrOutcome(node, io.EOF) != rrFailActive {
		t.Fatal("refused or reset at a leaf that goes to the site: explicit REFUSE_DST; anything else FAIL_NODE")
	}
	proxy := &cycleNode{tag: "p"}
	manager.byTag["p"] = proxy
	if siteOnOpenLx(context.Background(), manager, proxy) || !g.pathDead(proxy, syscall.ECONNREFUSED) || s.rrOutcome(proxy, syscall.ECONNREFUSED) != rrFailActive {
		t.Fatal("a proxy's refused open is a dead path, FAIL_NODE")
	}
	if s.rrOutcome(proxy, os.NewSyscallError("connect", syscall.EHOSTUNREACH)) != rrLocal || s.rrOutcome(proxy, os.ErrDeadlineExceeded) != rrFailTimeout ||
		s.rrOutcome(proxy, context.Canceled) != rrCancel {
		t.Fatal("LOCAL, the timeout, CANCEL")
	}
}

// rrGroupState is what the rules write: the history, the slots, the counts, the key
// shifts and marks, the K2 memory, and per node the proofs, the answers to keys,
// the suspicion, the Q1 signs and the life probes.
type rrGroupState struct {
	pool    string
	history map[string]*adapter.URLTestHistory
	counts  map[string]rrSlotCount
	shifts  int64
	k2      int64
	tracks  map[string][6]int64
	keys    map[string]int64
	running bool
}

func (f *healFixture) groupState() rrGroupState {
	b := f.s.balancer
	state := rrGroupState{
		pool:    strings.Join(f.pool(), ","),
		history: make(map[string]*adapter.URLTestHistory),
		counts:  make(map[string]rrSlotCount),
		shifts:  int64(b.memory.shifts.size.Load()),
		k2:      int64(b.memory.k2.size.Load()),
		tracks:  make(map[string][6]int64),
		keys:    make(map[string]int64),
		running: b.heal.roundBusy(),
	}
	for tag := range f.nodes {
		state.history[tag] = f.g.history.LoadURLTestHistory(tag)
	}
	b.memory.access.Lock()
	for tag, c := range b.memory.counts {
		state.counts[tag] = *c
	}
	b.memory.access.Unlock()
	for _, entry := range b.slotsSnapshot() {
		track := entry.track
		if track == nil {
			continue
		}
		var want int64
		if track.wantCheck.Load() {
			want = 1
		}
		state.tracks[entry.tag] = [6]int64{track.proved.Load(), track.suspectFrom.Load(), track.suspectUntil.Load(), want, int64(track.pendingN.Load()), track.lifeLast.Load()}
		var keys int64
		for i := range track.keyAnswers {
			keys += track.keyAnswers[i].Load()
		}
		state.keys[entry.tag] = keys
	}
	return state
}

// N0 (owner, 2026-11-25): in a freeze a failed open on the key's own slot —
// FAIL_NODE, an explicit refusal, LOCAL — opens through another slot, and the group
// writes nothing: no shift, no count, no K2 record, no suspicion, no probe, no
// history. After the freeze the same failure goes to its rules again.
func TestRRAlgo_N0_retryInTheFreezeChangesNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	heal := &f.s.balancer.heal
	local := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
	key := f.keyOn(t, "site.example", 0)
	heal.freeze()
	before := f.groupState()
	cases := []struct {
		name string
		set  func(on bool)
	}{
		{"FAIL_NODE", func(on bool) { f.fb["a"].siteDown.Store(on) }},
		{"explicit REFUSE_DST", func(on bool) { f.fb["a"].set(&f.fb["a"].openFail, "site.example", on) }},
		{"LOCAL", func(on bool) {
			if on {
				f.fb["a"].dialErr = local
			} else {
				f.fb["a"].dialErr = nil
			}
		}},
	}
	for _, c := range cases {
		dials := f.dials("b")
		c.set(true)
		opened, answered := f.dial(t, key, "site.example")
		c.set(false)
		if !opened || !answered || f.dials("b") != dials+1 {
			t.Fatalf("%s in a freeze: the retry through b answers", c.name)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if shift, marked := f.shift(key, "site.example"); shift != 0 || marked {
		t.Fatalf("N0: the key stays, shift %d marked %v", shift, marked)
	}
	if !reflect.DeepEqual(before, f.groupState()) || f.probes("a") != 0 || f.probes("b") != 0 {
		t.Fatalf("N0: the retry writes nothing\nbefore %+v\nafter  %+v", before, f.groupState())
	}
	heal.epochEnd.Store(rrClock(time.Now()))
	time.Sleep(2 * time.Millisecond)
	f.fb["a"].siteDown.Store(true)
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("after the freeze: the retry answers")
	}
	f.fb["a"].siteDown.Store(false)
	f.waitCheck(t)
	if f.probes("a") == 0 || reflect.DeepEqual(before, f.groupState()) {
		t.Fatal("after the freeze the failure goes to its rules: the life probe")
	}
}

// lx:end SPEC 138
