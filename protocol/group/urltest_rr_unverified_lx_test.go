// lx:begin SPEC 019, SPEC 138 round_robin: the pool nodes the manual test with no answer left unverified

package group

import (
	"context"
	"testing"
	"time"
)

// manualOutage runs the manual test of the group in an outage: every node is dead.
func (f *healFixture) manualOutage(t *testing.T) {
	t.Helper()
	for _, node := range f.nodes {
		node.dead.Store(true)
	}
	_, _ = f.g.URLTest(context.Background())
}

func (f *healFixture) unverified(tags ...string) bool {
	for _, tag := range tags {
		if !f.g.rrUnverified(tag) {
			return false
		}
	}
	return true
}

// lookAndWait is the look of the pick at the pool after the pause, and the round
// it starts.
func (f *healFixture) lookAndWait(t *testing.T) {
	t.Helper()
	f.s.balancer.heal.clearPause()
	f.s.rrPoolLook(context.Background())
	waitLong(t, func() bool { return !f.s.balancer.heal.roundBusy() }, 10*time.Second)
}

// The manual test with no answer leaves the slots of its pool unverified: no slot to
// fill, a second manual test in the same outage keeps them so, the replacement
// search gives none away and probes no candidate.
func TestRRUnverified_manualOutageKeepsSlots(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	f.manualOutage(t)
	if !f.unverified(tags[:3]...) || f.g.rrUnverified("d") {
		t.Fatal("the manual test with no answer did not leave its pool nodes unverified")
	}
	f.manualOutage(t)
	if !f.unverified(tags[:3]...) {
		t.Fatal("a second manual test in the outage lost the marks")
	}
	if f.s.rrSlotToFill() || !f.s.rrSlotToVerify() {
		t.Fatal("an unverified slot is a slot to fill")
	}
	for _, node := range f.nodes {
		node.dead.Store(false)
	}
	f.s.rrHealSearch() // the search comes first after the network is back
	if !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the search gave an unverified slot away: %v", f.pool())
	}
	for _, tag := range tags[3:] {
		if f.probes(tag) != 2 {
			t.Fatalf("the search probed the candidate %s: %d probes", tag, f.probes(tag))
		}
	}
}

// The network comes back after the manual test in an outage: the look of the pick
// verifies the pool with a control, the live pool keeps its slots and no candidate
// takes one.
func TestRRUnverified_networkBackPoolKept(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	f.manualOutage(t)
	for _, node := range f.nodes {
		node.dead.Store(false)
	}
	before := map[string]int32{}
	for _, tag := range tags[:3] {
		before[tag] = f.probes(tag)
	}
	f.lookAndWait(t)
	if !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the live pool lost a slot: %v", f.pool())
	}
	for _, tag := range tags[:3] {
		if f.probes(tag) <= before[tag] || f.g.history.LoadURLTestHistory(tag) == nil {
			t.Fatalf("the pool node %s was not verified", tag)
		}
	}
	if f.s.rrSlotToVerify() || f.s.rrSlotToFill() {
		t.Fatal("a slot is still unverified or to fill")
	}
}

// One pool node really died in the outage: the check strikes it out against the
// control, and a candidate that answered takes its slot; the live ones keep theirs.
func TestRRUnverified_deadNodeLosesSlot(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	f.manualOutage(t)
	for tag, node := range f.nodes {
		node.dead.Store(tag == "b")
	}
	f.lookAndWait(t)
	pool := f.pool()
	if pool[0] != "a" || pool[2] != "c" || pool[1] == "b" || pool[1] == "" {
		t.Fatalf("the slot of the dead node was not replaced in place: %v", pool)
	}
	if f.s.rrSlotToVerify() || f.g.rrUnverified("b") {
		t.Fatal("a mark outlived the verification")
	}
}

// The scheduled run comes first after the network is back: it probes the
// unverified nodes and keeps the live ones in their slots; a candidate that
// answers in its walk does not take the slot of an unverified node.
func TestRRUnverified_scheduledRunFirst(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	for _, dead := range []string{"", "b"} {
		t.Run("dead="+dead, func(t *testing.T) {
			f := newHealFixture(t, tags, tags[:3], 0)
			f.manualOutage(t)
			for tag, node := range f.nodes {
				node.dead.Store(tag == dead)
			}
			f.g.balancePool(context.Background())
			pool := f.pool()
			if f.s.rrSlotToVerify() {
				t.Fatal("a mark outlived the run with an answer")
			}
			if dead == "" {
				if !samePool(pool, tags[:3]...) {
					t.Fatalf("the live pool lost a slot: %v", pool)
				}
				return
			}
			if pool[0] != "a" || pool[2] != "c" || pool[1] == "b" || pool[1] == "" {
				t.Fatalf("the slot of the dead node was not replaced in place: %v", pool)
			}
		})
	}
}

// The scheduled run whose pool batch got no answer (the network came back during
// it) after the manual test in an outage: the walk answers, its fill gives no
// unverified slot away, the pool nodes are probed again with the walk as the
// control; only a dead one loses its slot, in place.
func TestRRUnverified_scheduledRunPoolBatchNoAnswer(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	for _, dead := range []string{"", "b"} {
		t.Run("dead="+dead, func(t *testing.T) {
			f := newHealFixture(t, tags, tags[:3], 0)
			f.manualOutage(t)
			for tag, node := range f.nodes {
				node.dead.Store(tag == dead)
			}
			for _, tag := range tags[:3] {
				f.nodes[tag].probeFails.Store(1) // the pool batch: no answer
			}
			f.g.balancePool(context.Background())
			pool := f.pool()
			if f.s.rrSlotToVerify() {
				t.Fatal("a mark outlived the run with an answer")
			}
			if dead == "" {
				if !samePool(pool, tags[:3]...) {
					t.Fatalf("the live pool lost a slot: %v", pool)
				}
				return
			}
			if pool[0] != "a" || pool[2] != "c" || pool[1] == "b" || pool[1] == "" {
				t.Fatalf("the slot of the dead node was not replaced in place: %v", pool)
			}
		})
	}
}

// A candidate that answers a probe of a run while the pool nodes are unverified:
// the fill of the run gives no unverified slot away.
func TestRRUnverified_fillSkipsUnverified(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	f.manualOutage(t)
	for _, tag := range tags[3:] {
		f.nodes[tag].dead.Store(false)
	}
	f.g.rrFillOnAlive("d")
	if !samePool(f.pool(), tags[:3]...) {
		t.Fatalf("the fill gave an unverified slot away: %v", f.pool())
	}
}

// The manual test with an answer: as before, no mark; the dead pool nodes lose
// their slots to live candidates.
func TestRRUnverified_manualWithAnswerNoMark(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, tags[:3], 0)
	for _, tag := range tags[:3] {
		f.nodes[tag].dead.Store(true)
	}
	_, _ = f.g.URLTest(context.Background())
	if f.s.rrSlotToVerify() {
		t.Fatal("the manual test with an answer left marks")
	}
	for _, tag := range f.pool() {
		if tag == "a" || tag == "b" || tag == "c" {
			t.Fatalf("a dead node kept its slot after the manual test with an answer: %v", f.pool())
		}
	}
}

// lx:end SPEC 019, SPEC 138
