// lx:begin SPEC 019, SPEC 138 the run of a round_robin group: no answer, no change

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
)

// R1: a scheduled run in a total outage (no probe of the run answers) changes
// nothing of the group: the layout, the history of the slots (no slot turns
// empty), the counters, strikes(n) and the quarantine stay as they were.
func TestRRRun_noAnswerChangesNothing(t *testing.T) {
	tags := []string{"n0", "n1", "n2", "n3", "n4"}
	f := newHealFixture(t, tags, tags[:3], 0)
	b := f.s.balancer
	b.rrQuarantine("n4")
	b.rrStruck("n1")
	b.memory.access.Lock()
	b.memory.count("n0").conns, b.memory.count("n0").fails = 10, 2
	b.memory.access.Unlock()
	for _, tag := range tags {
		f.nodes[tag].dead.Store(true)
	}
	result := f.g.balancePool(context.Background())
	if len(result) != 0 || f.allProbes() != 5 {
		t.Fatalf("precondition: the run probes every node and none answers: %v, %d probes", result, f.allProbes())
	}
	if !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the layout changed: %v", f.pool())
	}
	if !f.allHistory(tags...) {
		t.Fatal("the run erased history: its slots turn empty")
	}
	for _, tag := range tags[:3] {
		if !f.s.slotAlive(tag) {
			t.Fatalf("the slot of %s is no longer live", tag)
		}
	}
	b.memory.access.Lock()
	strikes, count := b.memory.strikes["n1"], *b.memory.count("n0")
	b.memory.access.Unlock()
	if !b.isQuarantined("n4") || strikes != 1 || count.conns != 10 || count.fails != 2 {
		t.Fatalf("the memory changed: quarantine %v, strikes %d, counters %d/%d", b.isQuarantined("n4"), strikes, count.conns, count.fails)
	}
	// The network is back: the next run lifts the quarantine at its first answer.
	for _, tag := range tags {
		f.nodes[tag].dead.Store(false)
	}
	f.g.balancePool(context.Background())
	if b.isQuarantined("n4") || !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the run with answers lifts the quarantine and keeps the live pool: %v", f.pool())
	}
}

// R1: the forced run after a network change with no answer neither lays out nor
// lifts, and erases no history.
func TestRRRun_networkRunNoAnswerChangesNothing(t *testing.T) {
	tags := []string{"n0", "n1", "n2", "n3", "n4"}
	f := newHealFixture(t, tags, tags[:3], 0)
	f.s.balancer.rrQuarantine("n4")
	for _, tag := range tags {
		f.nodes[tag].dead.Store(true)
	}
	if _, err := f.g.urlTest(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !samePool(f.pool(), tags[:3]...) || !f.allHistory(tags...) || !f.s.balancer.isQuarantined("n4") {
		t.Fatalf("the manual run with no answer changed the group: %v", f.pool())
	}
}

// R1, the manual test (owner, 2026-11-26): in an outage it erases the history of
// the nodes that failed it, as upstream, so they show as errors; it still lays out
// nothing and lifts nothing. The scheduled run in the same outage erases nothing.
func TestRRRun_manualNoAnswerErasesHistory(t *testing.T) {
	tags := []string{"n0", "n1", "n2", "n3", "n4"}
	for _, entry := range []struct {
		name string
		run  func(f *healFixture)
	}{
		{"URLTest", func(f *healFixture) { _, _ = f.g.URLTest(context.Background()) }},
		{"CheckOutbounds", func(f *healFixture) { f.s.ctx = f.g.ctx; f.s.CheckOutbounds() }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			f := newHealFixture(t, tags, tags[:3], 0)
			f.s.balancer.rrQuarantine("n4")
			for _, tag := range tags {
				f.nodes[tag].dead.Store(true)
			}
			f.g.balancePool(context.Background())
			if !f.allHistory(tags...) {
				t.Fatal("the scheduled run in an outage erased history")
			}
			entry.run(f)
			for _, tag := range tags {
				if f.g.history.LoadURLTestHistory(tag) != nil {
					t.Fatalf("the manual test in an outage kept the history of %s", tag)
				}
			}
			if !samePool(f.pool(), tags[:3]...) || !f.s.balancer.isQuarantined("n4") {
				t.Fatalf("the manual test with no answer changed the layout or the quarantine: %v", f.pool())
			}
		})
	}
}

// R1, the stand of impair 4a: the scheduled run starts in an outage, its pool batch
// gets no answer, the network comes back and the walk answers. The batch with no
// answer erases nothing, so the live pool keeps its nodes: no key moves.
func TestRRRun_outageAtRunStartKeepsPool(t *testing.T) {
	tags := []string{"n0", "n1", "n2", "n3", "n4"}
	f := newHealFixture(t, tags, tags[:3], 0)
	for _, tag := range tags[:3] {
		f.nodes[tag].probeFails.Store(1) // the outage: the first probe of each fails
	}
	result := f.g.balancePool(context.Background())
	if _, ok := result["n3"]; !ok {
		t.Fatalf("precondition: the walk answered after the outage: %v", result)
	}
	if !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the pool changed after an outage at the start of the run: %v", f.pool())
	}
	if !f.allHistory(tags[:3]...) {
		t.Fatal("the batch with no answer erased the history of the pool")
	}
	for _, tag := range tags[:3] {
		if f.probes(tag) != 2 {
			t.Fatalf("the run probes %s again once a later batch answered: %d probes", tag, f.probes(tag))
		}
	}
}

// R1 with a control: the node of a pool of 1 is dead and the walk answers — the
// probe of the node again, after that answer, fails, and the run replaces it.
func TestRRRun_deadPoolOfOneReplaced(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a"}, 0)
	f.nodes["a"].dead.Store(true)
	f.g.balancePool(context.Background())
	if !samePool(f.pool(), "b") || f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatalf("the dead node of a pool of 1 stays in its slot: %v", f.pool())
	}
}

// R1 with a control: the whole pool died at once, candidates are alive — the run
// replaces the pool.
func TestRRRun_deadPoolReplaced(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	for _, tag := range tags[:3] {
		f.nodes[tag].dead.Store(true)
	}
	f.g.balancePool(context.Background())
	if !samePool(f.pool(), "d", "e", "f") {
		t.Fatalf("a dead pool is replaced by the run: %v", f.pool())
	}
}

// §5 (Ж): a node struck out in the previous interval that answers the scheduled run
// is a candidate as any other: the run puts it into an empty slot.
func TestRRRun_struckBeforeRunIsCandidate(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.g.history.DeleteURLTestHistory("c")
	f.s.balancer.clearSlot("c")
	f.s.balancer.rrStruck("c") // D1 before the run
	f.g.balancePool(context.Background())
	if !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("the node struck out before the run answered it and takes the empty slot: %v", f.pool())
	}
}

// §5 (З, the live stand): a node of the walk took a hole during the run and was
// struck out (D1) before the run ended; another node of the walk had answered with
// no hole to take. The layout gives the hole to the next live node, not to the struck
// one (which would leave the slot empty).
func TestRRRun_layoutSkipsNodeStruckDuringRun(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	f.nodes["c"].dead.Store(true)
	f.nodes["e"].slow = 100 * time.Millisecond
	f.nodes["f"].dead.Store(true)
	f.nodes["f"].slow = time.Second // holds the run open after the D1
	// c died before the run: since upstream 37fffa325 a batch writes the history
	// of its probes only when it ends, so the hole d takes during the run has to
	// be there before the batch starts.
	f.g.history.DeleteURLTestHistory("c")
	done := make(chan struct{})
	go func() {
		f.g.balancePool(context.Background())
		close(done)
	}()
	waitUntil(t, func() bool {
		e := f.nodes["e"]
		return f.pool()[2] == "d" && e.probes.Load() > 0 && e.active.Load() == 0
	})
	time.Sleep(100 * time.Millisecond) // e's answer completes; f still holds the run for most of a second
	// D1 of d by a check beside the run, as rrStrike does it.
	f.g.history.DeleteURLTestHistoryNotAfterLx("d", time.Now())
	f.s.balancer.clearSlot("d")
	f.s.balancer.rrStrikeOut("d", true)
	<-done
	if !samePool(f.pool(), "a", "b", "e") {
		t.Fatalf("the hole goes to the next live node of the walk: %v", f.pool())
	}
}

// З: after the run, the replacement search polls first the candidates with live
// history (the reserve the run found), then the rest from the cursor. A failed
// self-heal probe of a candidate that ended after its answer to the run takes the
// liveness from that answer: the candidate goes by the cursor (§5, by the rule).
func TestRRRun_searchPollsLiveHistoryFirst(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	for _, tag := range tags[3:] {
		f.g.history.DeleteURLTestHistory(tag)
	}
	f.g.balancePool(context.Background())
	if !f.allHistory("d", "e") || f.g.history.LoadURLTestHistory("f") != nil {
		t.Fatal("precondition: the run walked to its reserve d, e")
	}
	f.s.balancer.heal.cursor = 6 // g
	batch := f.s.rrCandidateBatch(map[string]bool{"a": true, "b": true, "c": true}, 3)
	if got := outboundTags(batch); len(got) != 3 || got[2] != "g" || !(got[0] == "d" && got[1] == "e" || got[0] == "e" && got[1] == "d") {
		t.Fatalf("live history first, then the cursor: %v", got)
	}
	f.s.balancer.heal.cursor = 6
	f.s.balancer.heal.missed("d", time.Now())
	batch = f.s.rrCandidateBatch(map[string]bool{"a": true, "b": true, "c": true}, 3)
	if got := outboundTags(batch); len(got) != 3 || got[0] != "e" || got[1] != "g" || got[2] != "h" {
		t.Fatalf("a candidate whose self-heal probe failed after its answer goes by the cursor: %v", got)
	}
}

func outboundTags(outbounds []adapter.Outbound) []string {
	tags := make([]string, len(outbounds))
	for i, outbound := range outbounds {
		tags[i] = outbound.Tag()
	}
	return tags
}

// R1 in a mixed config: a least_test group shares the history of the core with a
// round_robin group. A batch of the round_robin run with no answer gives back only
// what its own probes erased: the history the least_test group's failed probes
// erased meanwhile stays erased, and least_test does not pick the dead node again.
func TestRRRun_noAnswerRestoresOnlyOwnErasure(t *testing.T) {
	tags := []string{"n0", "n1", "n2"}
	f := newHealFixture(t, tags, tags[:2], 0)
	least := &URLTestGroup{
		ctx:            context.Background(),
		outbound:       f.g.outbound,
		logger:         log.NewNOPFactory().NewLogger("least"),
		outbounds:      f.g.outbounds,
		link:           f.g.link,
		interval:       time.Minute,
		history:        f.g.history,
		interruptGroup: interrupt.NewGroup(),
	}
	for _, tag := range tags {
		f.nodes[tag].dead.Store(true)
	}
	batch := f.g.rrBatchBegin(f.g.outbounds)
	_, _ = least.urlTest(context.Background(), true) // the least_test run fails every node: its probes erase
	if f.g.history.LoadURLTestHistory("n0") != nil {
		t.Fatal("precondition: the least_test run erased n0")
	}
	f.g.rrBatchEnd(batch) // the round_robin batch ends with no answer
	for _, tag := range tags {
		if f.g.history.LoadURLTestHistory(tag) != nil {
			t.Fatalf("the batch with no answer gave back the history of %s the least_test group erased", tag)
		}
	}
	if selected, _ := least.Select(N.NetworkTCP); selected != nil && f.g.history.LoadURLTestHistory(selected.Tag()) != nil {
		t.Fatalf("least_test picks %s as alive", selected.Tag())
	}
	f.fresh(tags...)
	f.g.balancePool(context.Background()) // its own probes in the same outage: given back
	if !f.allHistory(tags...) {
		t.Fatal("the batch with no answer did not give back the history its own probes erased")
	}
}

// lx:end SPEC 019, SPEC 138
