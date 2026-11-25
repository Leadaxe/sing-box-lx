// lx:begin SPEC 138 round_robin self-heal: the rules of round 9 (ALGORITHM.md §4.2, §4.5–§4.7, §5)

package group

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// --- W: the pause of rounds ----------------------------------------------------------

// W1, W2, I10: the step grows only after a round with no ANSWER at a lap; with no
// lap the pause stays at the base.
func TestRR9_W2_growsOnlyAfterALap(t *testing.T) {
	var h rrHeal
	at := time.Now()
	for i := 0; i < 3; i++ {
		if !h.beginRound(at) {
			t.Fatalf("round %d: the base pause is over", i)
		}
		h.endRound(at, true, false, false)
		if h.pauseStep() != 0 {
			t.Fatal("W2: no lap, the step stays at the base")
		}
		if h.ready(at.Add(rrHealGap - time.Millisecond)) {
			t.Fatal("W1: no round inside the pause")
		}
		at = at.Add(rrHealGap)
	}
	for want := int32(1); want <= rrPauseSteps+1; want++ {
		if !h.beginRound(at) {
			t.Fatal("the pause is over")
		}
		h.endRound(at, true, false, true)
		if got := h.pauseStep(); got != min(want, rrPauseSteps) {
			t.Fatalf("W2: step %d, want %d", got, min(want, rrPauseSteps))
		}
		at = at.Add(h.pause())
	}
	if h.pause() != 120*time.Second {
		t.Fatalf("the ceiling is 120 s, got %v", h.pause())
	}
	h.beginRound(at)
	h.endRound(at, true, true, true)
	if h.pauseStep() != rrPauseSteps {
		t.Fatal("a round with an ANSWER does not grow the step; W3 resets it at the answer")
	}
}

// W3, I10: an ANSWER of a probe of the group and N3 reset the step; o restarts at
// the answer only.
func TestRR9_W3_resetByAnswerAndSignal(t *testing.T) {
	var h rrHeal
	h.setPauseStep(rrPauseSteps)
	h.setPollCount(7)
	end := time.Now()
	h.setPauseEnd(&end)
	h.probeAnswered()
	if h.pauseStep() != 0 || h.pollCount() != 0 || h.pause() != rrHealGap {
		t.Fatal("W3: an ANSWER brings the base back and restarts o")
	}
	h.setPauseStep(2)
	h.setPollCount(4)
	h.freeze()
	if h.pauseStep() != 0 || h.pollCount() != 4 || h.pauseEnd() != nil {
		t.Fatal("W3: N3 resets the step and lifts the pause, o stays")
	}
}

// W5: the first byte of a connection in a pause above the base gives one round at
// once, once per pause; a round of the right with no ANSWER keeps the step and the
// end of the pause; at the base there is no right.
func TestRR9_W5_oneExtraRoundPerPause(t *testing.T) {
	var h rrHeal
	at := time.Now()
	h.beginRound(at)
	h.endRound(at, true, false, false)
	if h.grantExtra(at.Add(time.Second), at.Add(time.Second)) {
		t.Fatal("W5: no right at the base")
	}
	h.setPauseStep(2)
	if !h.grantExtra(at.Add(time.Second), at.Add(time.Second)) || h.grantExtra(at.Add(2*time.Second), at.Add(2*time.Second)) {
		t.Fatal("W5: one right per pause")
	}
	end := h.pauseEnd()
	if !h.beginRound(at.Add(2 * time.Second)) {
		t.Fatal("W5: the right gives a round inside the pause")
	}
	h.endRound(at.Add(3*time.Second), true, false, true)
	if h.pauseEnd() != end || h.pauseStep() != 2 {
		t.Fatal("W5: the round of the right with no ANSWER keeps the step and the end of the pause")
	}
	if h.ready(at.Add(4*time.Second)) || h.grantExtra(at.Add(5*time.Second), at.Add(5*time.Second)) {
		t.Fatal("W5: the second first byte of the same pause gives no round")
	}
	if !h.ready(at.Add(h.pause())) {
		t.Fatal("the pause ends as it would")
	}
}

// W4: in a pause above the base no life probe starts; the bypass by a slow sign is
// set as usual (S2).
func TestRR9_W4_lifeProbeWaitsForThePause(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	heal.setPauseStep(1)
	end := time.Now()
	heal.setPauseEnd(&end)
	node, _ := f.s.outbound.Outbound("a")
	if f.s.rrLifeProbe(node, f.s.balancer.trackOf("a"), rrClock(time.Now()), false) {
		t.Fatal("W4: no life probe while the pause above the base goes on")
	}
	heal.setPauseStep(0)
	if !f.s.rrLifeProbe(node, f.s.balancer.trackOf("a"), rrClock(time.Now()), false) {
		t.Fatal("W4: at the base the life probe has its own limits only")
	}
	f.waitCheck(t)
}

// --- U: the probe queue of the core --------------------------------------------------

// U1, U2: the batches of a round are granted in the order the round asked for them:
// a part of decisive probes asked for after the first probes of a pool larger than
// U never overtakes them (its places would wait for those first probes, which
// would wait for its places).
func TestRR9_U_requestsKeepTheirOrder(t *testing.T) {
	f := newHealFixture(t, []string{"a"}, []string{"a"}, 0)
	q := &rrQueue{flights: make(map[rrFlightKey]*rrFlight)}
	ctx := context.Background()
	var other rrQueueOwner
	if !q.acquire(ctx, &other, rrQueueCap) {
		t.Fatal("precondition: the queue is full")
	}
	const n = 8
	c := &rrCheck{s: f.s, ctx: ctx, queue: q, grants: make(chan func(), n)}
	var order []int
	for i := range n {
		c.request(rrQueueCap, func() { order = append(order, i) })
	}
	for range n {
		q.release(rrQueueCap) // the places of the batch before: the next one is granted
		(<-c.grants)()
	}
	q.release(rrQueueCap)
	for i, got := range order {
		if got != i {
			t.Fatalf("the batches were granted out of the order asked: %v", order)
		}
	}
}

// U1, U2: at most U places held; a batch is granted whole; the groups waiting are
// served in turn, one batch each.
func TestRR9_U1U2_capAndTurns(t *testing.T) {
	q := &rrQueue{flights: make(map[rrFlightKey]*rrFlight)}
	ctx := context.Background()
	var a, b rrQueueOwner
	if !q.acquire(ctx, &a, rrQueueCap) || q.inFlight() != rrQueueCap {
		t.Fatal("U1: a batch of U is granted whole")
	}
	var order []string
	var access sync.Mutex
	var wg sync.WaitGroup
	ask := func(owner *rrQueueOwner, name string, places int) {
		defer wg.Done()
		if q.acquire(ctx, owner, places) {
			access.Lock()
			order = append(order, name)
			access.Unlock()
		}
	}
	wg.Add(3)
	go ask(&a, "a1", 10)
	time.Sleep(20 * time.Millisecond)
	go ask(&a, "a2", 10)
	time.Sleep(20 * time.Millisecond)
	go ask(&b, "b1", 10)
	time.Sleep(20 * time.Millisecond)
	if len(order) != 0 {
		t.Fatal("U1: no place, no grant")
	}
	q.release(rrQueueCap)
	waitUntil(t, func() bool { access.Lock(); defer access.Unlock(); return len(order) == 1 })
	if q.inFlight() > rrQueueCap {
		t.Fatal("U: never more than U")
	}
	q.release(10)
	waitUntil(t, func() bool { access.Lock(); defer access.Unlock(); return len(order) == 2 })
	q.release(10)
	wg.Wait()
	if order[0] != "a1" || order[1] != "b1" || order[2] != "a2" {
		t.Fatalf("U2: one batch per group in turn, got %v", order)
	}
	q.release(10)
	if q.inFlight() != 0 {
		t.Fatal("every place goes back")
	}
}

// U3: one probe per node in flight; a probe asked for with a bound not later than
// the start of the one flying takes its outcome, a later bound waits and starts its
// own.
func TestRR9_U3_oneProbePerNode(t *testing.T) {
	q := &rrQueue{flights: make(map[rrFlightKey]*rrFlight)}
	key := rrFlightKey{leaf: "a", link: "u"}
	var runs atomic.Int32
	release := make(chan struct{})
	run := func(context.Context) rrProbe {
		runs.Add(1)
		<-release
		return rrProbe{answered: true}
	}
	ctx := context.Background()
	before := time.Now()
	results := make(chan rrProbe, 3)
	go func() { results <- q.probe(ctx, key, before, run) }()
	waitUntil(t, func() bool { return runs.Load() == 1 })
	go func() { results <- q.probe(ctx, key, before, run) }()
	time.Sleep(20 * time.Millisecond)
	late := time.Now()
	go func() { results <- q.probe(ctx, key, late, run) }()
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("U3: one probe of the node in flight")
	}
	close(release)
	for i := 0; i < 3; i++ {
		if r := <-results; !r.answered {
			t.Fatal("every asker gets an outcome")
		}
	}
	if runs.Load() != 2 {
		t.Fatalf("U3: the later bound started its own probe after the first ended: %d runs", runs.Load())
	}
}

// The queue belongs to the core instance: one per history storage.
func TestRR9_U_queuePerCore(t *testing.T) {
	first, second := urltest.NewHistoryStorage(), urltest.NewHistoryStorage()
	if rrQueueOf(first) != rrQueueOf(first) || rrQueueOf(first) == rrQueueOf(second) {
		t.Fatal("one queue per core instance")
	}
}

// --- §4.2: the decisive probe ------------------------------------------------------

// R3: a healthy pool of 50 gets one probe per node: the decisive probe goes only to
// a node whose first probe ended with no ANSWER or flew H (step 3).
func TestRR9_decisiveOnlyToSilentNodes(t *testing.T) {
	tags := make([]string, 50)
	for i := range tags {
		tags[i] = "n" + strconv.Itoa(i)
	}
	f := newHealFixture(t, tags, tags, 0)
	f.runCheck()
	total := 0
	for _, tag := range tags {
		total += int(f.probes(tag))
	}
	if total != 50 {
		t.Fatalf("R3: one probe per live node, got %d", total)
	}
	if q := f.g.rrQueue(); q.inFlight() != 0 {
		t.Fatalf("every place goes back, %d held", q.inFlight())
	}
}

// --- Q4 ------------------------------------------------------------------------------

// Q4: the second D1 since the scheduled run is the quarantine: the node is no
// candidate until the run, which lifts it and restarts the count.
func TestRR9_Q4_secondStrikeQuarantines(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b"}, 0)
	f.nodes["c"].dead.Store(true)
	f.nodes["a"].probeFails.Store(2)
	f.runCheck()
	if !samePool(f.pool(), "", "b") || f.s.balancer.isQuarantined("a") {
		t.Fatal("the first D1: the slot is empty, no quarantine")
	}
	f.runSearch()
	if !samePool(f.pool(), "a", "b") {
		t.Fatal("a answered its own probe and came back")
	}
	f.nodes["a"].probeFails.Store(2)
	f.runCheck()
	if !samePool(f.pool(), "", "b") || !f.s.balancer.isQuarantined("a") {
		t.Fatal("Q4: the second D1 quarantines a")
	}
	probes := f.probes("a")
	f.runSearch()
	if f.probes("a") != probes || !samePool(f.pool(), "", "b") {
		t.Fatal("Q4: a quarantined node is no candidate")
	}
	f.s.balancer.rrScheduledRun()
	if f.s.balancer.isQuarantined("a") || f.s.balancer.memory.strikes["a"] != 0 {
		t.Fatal("the scheduled run lifts the quarantine and restarts strikes(n)")
	}
}

// Q4: with no other node in the pool there is no quarantine (the guard of a small
// pool, as Q2).
func TestRR9_Q4_aloneNotQuarantined(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a"}, 0)
	f.s.balancer.memory.strikes = map[string]int{"a": 1}
	f.nodes["a"].probeFails.Store(2)
	f.nodes["c"].dead.Store(true)
	f.runCheck()
	if f.s.balancer.isQuarantined("a") {
		t.Fatal("Q4 needs another node in the pool")
	}
}

// --- §2: the client finished ---------------------------------------------------------

// R5: the end of the client's reading before the first byte from the node is
// CANCEL: the exchange leaves the wait for SILENT and the bypass of S2.
func TestRR9_clientFinishedIsCancel(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	conn, _ := f.watchedOn(t, f.keyOn(t, "site.example", 0), "site.example", "a")
	track := f.s.balancer.trackOf("a")
	if track.waits.oldest.Load() == 0 {
		t.Fatal("precondition: the connection waits for its first byte")
	}
	conn.LxClientFinished(true)
	age(conn, 2*rrProbeTimeout())
	now := rrClock(time.Now())
	f.s.rrInspect(track, now)
	if track.waits.oldest.Load() != 0 || conn.state.Load()&rrStateSilent != 0 || track.suspect(now, f.s.balancer.heal.epoch()) {
		t.Fatal("CANCEL: no wait, no SILENT, no bypass")
	}
}

// --- §5: the retry of the outermost group ------------------------------------------

// R6: an open marked by an outer round_robin group is not retried by the inner one.
func TestRR9_onlyTheOuterGroupRetries(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	destination := M.ParseSocksaddrHostPort("example.org", 443)
	ctx := rrMarkDial(context.Background())
	picked := f.s.testPick(ctx, destination, time.Now())
	selected, _ := f.s.outbound.Outbound("a")
	f.s.rrDialStart(ctx, selected, &picked)
	if !picked.inner {
		t.Fatal("the mark of the outer group is found")
	}
	if _, ok := f.s.rrHealDial(ctx, N.NetworkTCP, destination, f.nodes["a"], E.New("dial failed"), picked); ok {
		t.Fatal("R6: the inner group does not retry")
	}
	if f.nodes["b"].dials.Load()+f.nodes["c"].dials.Load() != 0 {
		t.Fatal("R6: no second open")
	}
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if !ok {
		t.Fatal("the outermost group retries")
	}
	f.waitCheck(t)
}

// --- L3: the counts of probes ------------------------------------------------------

// virtualRounds drives the self-heal of f in virtual time over [from, to), a step
// a second: each second the life probes the limits and W4 allow are claimed for
// every pool node (a sign of traffic on each, the worst rate), the right of W5 is
// granted when firstBytes, then a round (round) starts when the pause allows it.
// The fake probes end at once, so a round ends at its own virtual second. It
// returns the probes of the rounds (counted on the fake nodes) and the life probes
// claimed.
func virtualRounds(f *healFixture, origin time.Time, from, to int, firstBytes bool, round func()) (rounds, life int) {
	heal := &f.s.balancer.heal
	for second := from; second < to; second++ {
		at := origin.Add(time.Duration(second) * time.Second)
		for _, entry := range f.s.balancer.slotsSnapshot() {
			if entry.track == nil {
				continue
			}
			if !(heal.pauseStep() > 0 && heal.pausing(at)) && heal.claimLife(entry.track, rrClock(at), false) {
				life++
				entry.track.lifeBusy.Store(false)
			}
		}
		if firstBytes {
			heal.grantExtra(at, at)
		}
		if !heal.beginRound(at) {
			continue
		}
		before := f.allProbes()
		end := heal.pauseEnd()
		round()
		rounds += int(f.allProbes() - before)
		if heal.pauseEnd() != end {
			heal.setPauseEnd(&at) // the round ended at its virtual second
		}
	}
	return rounds, life
}

func (f *healFixture) allProbes() int32 {
	var total int32
	for _, node := range f.nodes {
		total += node.probes.Load()
	}
	return total
}

// deadGroup is a group of size nodes with the pool of the first pool ones, every
// node dead, no history.
func deadGroup(t *testing.T, size, pool int) *healFixture {
	tags := make([]string, size)
	for i := range tags {
		tags[i] = "n" + strconv.Itoa(i)
	}
	f := newHealFixture(t, tags, tags[:pool], 0)
	for i, tag := range tags {
		f.nodes[tag].dead.Store(true)
		if i >= pool {
			f.g.history.DeleteURLTestHistory(tag) // the pool died after its history (a slot without one is empty, §5)
		}
	}
	return f
}

// L3, С11, scenario 4: pool 3, everything dead, probes end at once. At the base
// (a big group, the cursor goes no lap within the minute) a minute takes at most
// 4·|P|(k+1) + 16 = 52; at the ceiling (a small group, the lap at once) a cycle of
// 120 s takes at most 2·|P|(k+1) rounds' probes and 4 life probes.
func TestRR9_L3_poolOfThreeAllDead(t *testing.T) {
	origin := time.Now()
	f := deadGroup(t, 503, 3)
	rounds, life := virtualRounds(f, origin, 0, 60, true, f.s.rrHealCheck)
	t.Logf("base: %d probes of rounds + %d life probes in a minute (L3: 52)", rounds, life)
	if rounds+life > 4*3*(rrCandidateBatches+1)+16 || f.s.balancer.heal.pauseStep() != 0 {
		t.Fatalf("L3 at the base: %d + %d", rounds, life)
	}

	origin = time.Now()
	f = deadGroup(t, 8, 3)
	virtualRounds(f, origin, 0, 300, true, f.s.rrHealCheck)
	if f.s.balancer.heal.pause() != 120*time.Second {
		t.Fatalf("the pause reached the ceiling: %v", f.s.balancer.heal.pause())
	}
	rounds, life = virtualRounds(f, origin, 300, 540, true, f.s.rrHealCheck)
	t.Logf("ceiling with first bytes: %d probes of rounds + %d life probes in 4 min (L3: 2·(2·9 + 4) = 44)", rounds, life)
	if rounds+life > 2*(2*3*(rrCandidateBatches+1)+4) {
		t.Fatalf("L3 at the ceiling: %d + %d in 4 min", rounds, life)
	}
	origin = time.Now()
	g := deadGroup(t, 8, 3)
	virtualRounds(g, origin, 0, 300, false, g.s.rrHealCheck)
	rounds, life = virtualRounds(g, origin, 300, 540, false, g.s.rrHealCheck)
	t.Logf("ceiling with no traffic: %d probes of rounds + %d life probes in 4 min (L3: 2·13 = 26)", rounds, life)
	if rounds+life > 2*(3*(rrCandidateBatches+1)+4) {
		t.Fatalf("L3 at the ceiling with no traffic: %d + %d in 4 min", rounds, life)
	}
}

// L3, С15, I9: the pool is the whole group of 50 and one node is dead; its slot is
// empty. The search probes only the struck node, no pool node; the pause grows at
// once (C holds one node: a lap per round).
func TestRR9_L3_poolIsTheGroupOneDead(t *testing.T) {
	tags := make([]string, 50)
	for i := range tags {
		tags[i] = "n" + strconv.Itoa(i)
	}
	f := newHealFixture(t, tags, tags, 0)
	f.nodes["n0"].dead.Store(true)
	f.g.history.DeleteURLTestHistory("n0")
	f.s.balancer.clearSlot("n0")
	origin := time.Now()
	first, _ := virtualRounds(f, origin, 0, 60, false, f.s.rrHealSearch)
	total, _ := virtualRounds(f, origin, 60, 600, false, f.s.rrHealSearch)
	t.Logf("pool 50/50, one dead: %d probes in the first minute, %d in the next 9 minutes (was ≈ 570 a minute)", first, total)
	for _, tag := range tags[1:] {
		if f.probes(tag) != 0 {
			t.Fatalf("I9: no probe of a pool node (%s)", tag)
		}
	}
	if first > 2 || total > 6 {
		t.Fatalf("the search alone with the growing pause: %d, %d", first, total)
	}
}

// L3, С16: five groups over one core with silent probes: never more than U probes
// in flight on the core.
func TestRR9_L3_fiveGroupsCoreBound(t *testing.T) {
	shortProbes(t, 300*time.Millisecond)
	shared := urltest.NewHistoryStorage()
	for i := 0; i < 3; i++ { // the pool nodes died after their history (§5)
		shared.StoreURLTestHistory("n"+strconv.Itoa(i), &adapter.URLTestHistory{Time: time.Now().Add(-time.Minute), Delay: 100})
	}
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	var fixtures []*healFixture
	for i := 0; i < 5; i++ {
		f := deadGroup(t, 20, 3)
		f.g.history = shared
		for _, node := range f.nodes {
			node.dead.Store(false)
			node.hold = never
		}
		fixtures = append(fixtures, f)
	}
	active := func() int32 {
		var total int32
		for _, f := range fixtures {
			for _, node := range f.nodes {
				total += node.active.Load()
			}
		}
		return total
	}
	start := time.Now()
	for _, f := range fixtures {
		if !f.s.rrStartRound(f.s.rrHealCheck) {
			t.Fatal("precondition: the checks start")
		}
	}
	var peak int32
	for {
		if n := active(); n > peak {
			peak = n
		}
		idle := true
		for _, f := range fixtures {
			if f.s.balancer.heal.roundBusy() {
				idle = false
			}
		}
		if idle {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	var probes int32
	for _, f := range fixtures {
		probes += f.allProbes()
	}
	elapsed := time.Since(start)
	t.Logf("5 groups, silent probes (T = 300 ms): %d probes, peak %d in flight, %v (core bound: U = 16 in flight, U·span/T)", probes, peak, elapsed.Round(time.Millisecond))
	if peak > rrQueueCap || probes != 5*3*(rrCandidateBatches+1) {
		t.Fatalf("U: peak %d, probes %d", peak, probes)
	}
	if bound := int32(rrQueueCap) * int32(elapsed/rrProbeTimeout()+1); probes > bound {
		t.Fatalf("the core bound: %d probes in %v", probes, elapsed)
	}
	if q := rrQueueOf(shared); q.inFlight() != 0 {
		t.Fatalf("every place goes back, %d held", q.inFlight())
	}
}

// lx:end SPEC 138
