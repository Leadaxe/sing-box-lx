// lx:begin SPEC 138 round_robin self-heal: the fixes of review 9 (ALGORITHM.md §4.2, §4.6, §4.7, §5)

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// waitLong waits up to d for cond.
func waitLong(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// U3: a flight cancelled because its askers left is not joined by a new asker; the
// new one starts a probe of its own.
func TestRRReview9_cancelledFlightNotJoined(t *testing.T) {
	q := rrQueueOf(urltest.NewHistoryStorage())
	key := rrFlightKey{leaf: "n", link: "u"}
	run := func(ctx context.Context) rrProbe {
		started := time.Now()
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // the dial notices the cancel a little later
		return rrProbe{err: ctx.Err(), started: started, ended: time.Now()}
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	go q.probe(ctxA, key, time.Time{}, run)
	time.Sleep(10 * time.Millisecond)
	cancelA()
	time.Sleep(10 * time.Millisecond)
	answered := func(ctx context.Context) rrProbe { return rrProbe{answered: true, started: time.Now()} }
	got := q.probe(context.Background(), key, time.Time{}, answered)
	if !got.answered {
		t.Fatalf("a new asker took the outcome of a flight cancelled by the others: err=%v", got.err)
	}
}

// U3, D1: a cancelled decisive probe is no "no ANSWER": its part gets no control
// after for it and the node is not struck.
func TestRRReview9_cancelledDecisiveIsNoMiss(t *testing.T) {
	c := &rrCheck{
		queue:    rrQueueOf(urltest.NewHistoryStorage()),
		nodes:    []rrPoolProbe{{firstEnded: true}},
		parts:    []*rrDecisivePart{{open: 1}},
		reserved: 1,
	}
	c.take(rrCheckResult{role: rrRoleDecisive, index: 0, probe: rrProbe{err: context.Canceled}})
	if c.parts[0].missed || !c.nodes[0].voided || c.flying != 0 {
		t.Fatalf("a cancelled decisive probe counts as a miss: missed=%v voided=%v flying=%d", c.parts[0].missed, c.nodes[0].voided, c.flying)
	}
}

// Step 3, U3: the decisive probe of a hung node starts H after its first probe,
// beside it, not after the first one ended.
func TestRRReview9_decisiveBesideFirstProbe(t *testing.T) {
	old := rrProbeTimeoutNs.Load()
	rrProbeTimeoutNs.Store(int64(8 * time.Second))
	t.Cleanup(func() { rrProbeTimeoutNs.Store(old) })
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.nodes["c"].hold = make(chan struct{})
	if !f.s.rrStartRound(f.s.rrHealCheck) {
		t.Fatal("no check")
	}
	time.Sleep(rrHangAfter + 1500*time.Millisecond)
	probes := f.nodes["c"].probes.Load()
	close(f.nodes["c"].hold)
	waitLong(t, func() bool { return !f.s.balancer.heal.roundBusy() }, 20*time.Second)
	if probes < 2 {
		t.Fatalf("no decisive probe of c at x+H while its first probe hangs: %d probes", probes)
	}
}

// N3: a group that takes the outcome of a probe of another group (U3) compares its
// own clocks too.
func TestRRReview9_joinedGroupComparesClocks(t *testing.T) {
	a := newHealFixture(t, []string{"n", "b"}, []string{"n", "b"}, 0)
	b := newHealFixture(t, []string{"n", "b"}, []string{"n", "b"}, 0)
	b.g.history, b.g.link = a.g.history, a.g.link
	a.nodes["n"].hold = make(chan struct{})
	done := make(chan struct{})
	go func() {
		a.g.rrSelfProbe(context.Background(), a.nodes["n"], time.Time{}, false)
		close(done)
	}()
	waitLong(t, func() bool { return a.nodes["n"].active.Load() == 1 }, 2*time.Second)
	now := time.Now()
	b.s.balancer.clockMono, b.s.balancer.clockWall = rrClock(now), now.Add(-time.Minute).UnixNano()
	joined := make(chan rrProbe, 1)
	go func() { joined <- b.g.rrSelfProbe(context.Background(), b.nodes["n"], time.Time{}, false) }()
	time.Sleep(50 * time.Millisecond)
	close(a.nodes["n"].hold)
	<-done
	probe := <-joined
	if b.nodes["n"].probes.Load() != 0 || !probe.answered {
		t.Fatalf("precondition: b takes the outcome of a's flight (probes %d, answered %v)", b.nodes["n"].probes.Load(), probe.answered)
	}
	if !b.s.balancer.heal.frozen() {
		t.Fatal("the joined group did not compare its clocks at the outcome")
	}
}

// §5: a candidate whose probe failed loses the liveness of its history (kept, not
// erased) and goes by the cursor: dead candidates with old history do not take every
// batch, and the cursor reaches a live candidate with no history.
func TestRRReview9_staleHistoryGoesByCursor(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	for _, tag := range []string{"a", "d", "e", "f", "g", "h", "i"} {
		f.nodes[tag].dead.Store(true)
	}
	f.g.history.DeleteURLTestHistory("j") // live, but no history: reached by the cursor only
	f.s.balancer.clearSlot("a")
	heal := &f.s.balancer.heal
	for round := 0; round < 4 && f.nodes["j"].probes.Load() == 0; round++ {
		heal.clearPause()
		if !f.s.rrStartSearch() {
			t.Fatalf("round %d did not start", round)
		}
		waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	}
	if f.nodes["j"].probes.Load() == 0 {
		t.Fatal("the live candidate j is never polled: dead candidates with old history take every batch")
	}
	if f.g.history.LoadURLTestHistory("d") == nil {
		t.Fatal("a failed probe of a candidate erased its history (an inference with no control)")
	}
	if !f.s.balancer.inPool("j") {
		t.Fatalf("j answered and takes the empty slot: %v", f.s.balancer.poolTags())
	}
}

// §5: a replacement search takes the flag of the run for the write of a slot only:
// a manual test during the probes of the search runs.
func TestRRReview9_searchLeavesManualTest(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e", "f"}, []string{"a", "b", "c"}, 0)
	for _, tag := range []string{"a", "d", "e", "f"} {
		f.nodes[tag].hold = make(chan struct{})
	}
	f.s.balancer.clearSlot("a")
	held := func() int32 {
		var active int32
		for _, tag := range []string{"a", "d", "e", "f"} {
			active += f.nodes[tag].active.Load()
		}
		return active
	}
	if !f.s.rrStartSearch() {
		t.Fatal("no search")
	}
	// The search probes its batch of candidates: they hang on hold.
	waitLong(t, func() bool { return held() >= 3 }, 10*time.Second)
	results := make(chan map[string]uint16, 1)
	go func() {
		result, _ := f.g.URLTest(context.Background())
		results <- result
	}()
	// The manual test is in flight beside the search: its own probes of the four
	// candidates hang too (or it returned at once, refused by the flag).
	waitLong(t, func() bool { return held() >= 3+4 || len(results) > 0 }, 10*time.Second)
	for _, tag := range []string{"a", "d", "e", "f"} {
		close(f.nodes[tag].hold)
	}
	if result := <-results; len(result) == 0 {
		t.Fatal("the manual test returned nothing while the search probed its candidates")
	}
	waitLong(t, func() bool { return !f.s.balancer.heal.roundBusy() }, 20*time.Second)
}

// W1: a search that starts while a run holds its flag probes nothing; it is no
// round — no pause, no growth of the step.
func TestRRReview9_searchUnderRunIsNoRound(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.s.balancer.clearSlot("a") // C = {a}: it has history, so the walk is 0 and lap holds
	heal := &f.s.balancer.heal
	f.g.checking.Store(true) // a scheduled run in flight
	for i := 0; i < 3; i++ {
		heal.clearPause()
		f.s.rrStartSearch()
		waitLong(t, func() bool { return !heal.roundBusy() }, 5*time.Second)
	}
	// The round already claimed when the run took the flag.
	if !heal.beginRound(time.Now()) {
		t.Fatal("no round")
	}
	f.s.rrHealSearch()
	f.g.checking.Store(false)
	if f.nodes["a"].probes.Load() != 0 {
		t.Fatalf("precondition: nothing probed, a probed %d", f.nodes["a"].probes.Load())
	}
	if heal.pauseStep() != 0 || heal.pausing(time.Now()) {
		t.Fatalf("rounds that probed nothing grew the step (%d) or started a pause", heal.pauseStep())
	}
}

// W1, W4: with no candidate (the third node in quarantine, the pool is the group)
// the slot to fill starts no round, the step stays at the base and the life probes
// of the pool go on.
func TestRRReview9_noCandidateNoRound(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.s.balancer.rrQuarantine("a")
	heal := &f.s.balancer.heal
	for i := 0; i < 3; i++ {
		heal.clearPause() // the pause is over
		f.s.rrPoolLook(context.Background())
		waitLong(t, func() bool { return !heal.roundBusy() }, 5*time.Second)
	}
	if heal.pauseStep() != 0 || heal.pausing(time.Now()) {
		t.Fatalf("searches with nothing to probe grew the step (%d) or started a pause", heal.pauseStep())
	}
	node, _ := f.s.outbound.Outbound("b")
	if !f.s.rrLifeProbe(node, f.s.balancer.trackOf("b"), rrClock(time.Now()), false) {
		t.Fatal("the life probe of b did not start (W4)")
	}
	f.waitCheck(t)
}

// §5, I9: D1 during a scheduled run — the plan of the run does not put the struck
// node back into its slot; the slot stays empty and asks for the search.
func TestRRReview9_runKeepsStruckNodeOut(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.nodes["c"].dead.Store(true)
	f.nodes["c"].slow = 300 * time.Millisecond
	done := make(chan struct{})
	go func() {
		f.g.CheckOutbounds(context.Background(), false) // the lazy scheduled run
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	// What rrStrike does to c meanwhile (D1 by a check beside the run).
	f.g.history.DeleteURLTestHistoryNotAfterLx("c", time.Now())
	f.s.balancer.clearSlot("c")
	f.s.balancer.rrStruck("c")
	<-done
	for _, tag := range f.s.balancer.poolTags() {
		if tag == "c" {
			t.Fatalf("the struck node is back in a slot: %v", f.s.balancer.poolTags())
		}
	}
	if !f.s.rrSlotToFill() {
		t.Fatal("the slot of the struck node is not to fill")
	}
}

// §5, I9: a slot whose node has no history (a dead occupant the run left, a leaf
// whose history D1 of another group erased) is an empty slot: no pool node of the
// check, and the search replaces it.
func TestRRReview9_slotWithoutHistoryIsEmpty(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	f.g.history.DeleteURLTestHistory("c")
	if !f.s.rrSlotToFill() {
		t.Fatal("a slot with a node without history is not to fill")
	}
	f.s.rrPoolLook(context.Background())
	waitLong(t, func() bool { return !f.s.balancer.heal.roundBusy() }, 5*time.Second)
	if f.nodes["c"].probes.Load() != 0 {
		t.Fatalf("the search probed the node of the slot (I9): %d", f.nodes["c"].probes.Load())
	}
	if pool := f.s.balancer.poolTags(); pool[2] != "d" {
		t.Fatalf("the candidate d does not take the slot: %v", pool)
	}
	f.g.history.DeleteURLTestHistory("b")
	f.s.balancer.heal.clearPause() // the pause after the search is over
	if !f.s.rrStartRound(f.s.rrHealCheck) {
		t.Fatal("no check")
	}
	waitLong(t, func() bool { return !f.s.balancer.heal.roundBusy() }, 10*time.Second)
	if f.nodes["b"].probes.Load() != 0 {
		t.Fatalf("the check probed the node of a slot without history: %d", f.nodes["b"].probes.Load())
	}
}

// R6, §5: a round_robin group B as a member of a round_robin group A — A does not
// retry its nested member and leaves no mark; B retries: one open and one retry.
func TestRRReview9_nestedRoundRobinRetriesOnce(t *testing.T) {
	a := newHealFixture(t, []string{"q"}, nil, 0)
	b := newHealFixture(t, []string{"x", "y"}, []string{"x", "y"}, 0)
	b.s.Adapter = outbound.NewAdapter(C.TypeURLTest, "B", []string{N.NetworkTCP}, []string{"x", "y"})
	a.g.outbound.(*fakeManager).byTag["B"] = b.s
	a.s.balancer.setSlots([]string{"B", "q"})
	destination := M.ParseSocksaddrHostPort("example.org", 443)
	pickedA := a.s.testPick(context.Background(), destination, time.Now())
	ctx := a.s.rrDialStart(context.Background(), b.s, &pickedA)
	if rrInnerDial(ctx) {
		t.Fatal("the outer group marked the open of its nested round_robin member")
	}
	pickedB := b.s.testPick(ctx, destination, time.Now())
	b.s.rrDialStart(ctx, b.nodes["x"], &pickedB)
	conn, ok := b.s.rrHealDial(ctx, N.NetworkTCP, destination, b.nodes["x"], E.New("dial failed"), pickedB)
	closeConn(conn)
	if !ok || b.nodes["y"].dials.Load() != 1 {
		t.Fatalf("the nested group does not retry: ok=%v y=%d", ok, b.nodes["y"].dials.Load())
	}
	if _, ok := a.s.rrHealDial(ctx, N.NetworkTCP, destination, b.s, E.New("dial failed"), pickedA); ok || a.nodes["q"].dials.Load() != 0 {
		t.Fatal("the outer group retries its nested member: a second retry")
	}
	b.waitCheck(t)
	a.waitCheck(t)
}

// Step 2, L3: a part of the first probes (a pool larger than U) granted after a
// batch of candidates started keeps its own batch: its outcome does not count
// against the batch of candidates (c.left).
func TestRRReview9_lateFirstPartKeepsItsBatch(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b"}, []string{"a"}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &rrCheck{
		s: f.s, ctx: ctx, queue: f.g.rrQueue(), x: time.Now(),
		results: make(chan rrCheckResult, 2), grants: make(chan func(), 2),
		nodes: make([]rrPoolProbe, 1),
	}
	c.launchAll(rrRoleFirst, 0, []adapter.Outbound{f.nodes["a"]})
	c.batches = 1 // a batch of candidates started before the part was granted
	then := <-c.grants
	c.flying--
	then()
	r := <-c.results
	if r.batch != 0 {
		t.Fatalf("the first probe counts against batch %d of the candidates", r.batch)
	}
}

// lx:end SPEC 138
