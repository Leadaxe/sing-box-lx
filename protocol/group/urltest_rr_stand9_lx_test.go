// lx:begin SPEC 138 round_robin self-heal: the findings of stand 9 (ALGORITHM.md §4.6, §5)

package group

import (
	"fmt"
	"testing"
	"time"
)

// §5, I5: the layout at the end of a run moves no node that took a slot during it.
// z, last in the config, answers first and takes the dead slot of d1, x the one of
// d2; the plan of the run, in walk order x, y, z, used to put x and y there and
// send z to the reserve.
func TestRRStand9_runLayoutKeepsMidRunFill(t *testing.T) {
	for _, tolerance := range []uint16{0, 60000} {
		t.Run(fmt.Sprint("tolerance ", tolerance), func(t *testing.T) {
			f := newHealFixture(t, []string{"d1", "d2", "a", "x", "y", "z"}, []string{"d1", "d2", "a"}, tolerance)
			for _, tag := range []string{"d1", "d2"} {
				f.nodes[tag].dead.Store(true)
				f.g.history.DeleteURLTestHistory(tag)
			}
			f.nodes["x"].slow = 200 * time.Millisecond
			f.nodes["y"].slow = 400 * time.Millisecond
			f.g.CheckOutbounds(f.g.ctx, false)
			if !samePool(f.pool(), "z", "x", "a") {
				t.Fatalf("z and x keep the slots they took during the run: %v", f.pool())
			}
		})
	}
}

// W5, L3: the pick that starts a search gives no round of the right when its
// connection answers: the dead candidate is probed once, not twice in the same
// second. A connection picked in the pause still gives the one round of the right
// (С15), and only one.
func TestRRStand9_searchPickGivesNoExtraRound(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	f.nodes["a"].dead.Store(true)
	f.s.balancer.clearSlot("a") // D1: a left its slot and its history
	f.g.history.DeleteURLTestHistory("a")
	answer := func(picked time.Time) {
		f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "b", track: f.s.balancer.trackOf("b"), start: picked}, nil)
	}
	picked := time.Now()
	if !f.s.rrStartSearch() {
		t.Fatal("precondition: the empty slot starts the search")
	}
	waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	if f.probes("a") != 1 || heal.pauseStep() != 1 {
		t.Fatalf("precondition: one probe of a, the lap grows the step: probes %d, step %d", f.probes("a"), heal.pauseStep())
	}
	answer(picked)
	if heal.roundBusy() || heal.extraOpen() || f.probes("a") != 1 {
		t.Fatalf("the connection picked before the pause gives no round: probes of a %d", f.probes("a"))
	}
	answer(time.Now())
	waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	if f.probes("a") != 2 {
		t.Fatalf("a connection picked in the pause gives the round of the right: probes of a %d", f.probes("a"))
	}
	answer(time.Now())
	if heal.roundBusy() || f.probes("a") != 2 {
		t.Fatal("one round of the right per pause")
	}
}

// W2: with the network silent the step grows after every round once each candidate
// was polled — also the ones polled first by the live history of the starting run:
// 30, 60, 120 s, not one round later.
func TestRRStand9_pauseStepsWithSilentNetwork(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e"}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	for _, tag := range tags {
		f.nodes[tag].dead.Store(true)
	}
	f.fresh("d", "e") // the starting run: the candidates have live history
	heal := &f.s.balancer.heal
	for round, want := range []int32{1, 2, 3, 3} {
		f.runCheck()
		if heal.pauseStep() != want {
			t.Fatalf("check %d: step %d, want %d (pause %v)", round+1, heal.pauseStep(), want, heal.pause())
		}
	}
}

// W2, С12: in a big group the step stays at the base until the cursor polled every
// candidate, the ones with live history included, each counted once.
func TestRRStand9_bigGroupStepWaitsForTheLap(t *testing.T) {
	tags := []string{"a", "b", "c"}
	for i := range 30 {
		tags = append(tags, fmt.Sprintf("c%02d", i))
	}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	for i, tag := range tags {
		f.nodes[tag].dead.Store(true)
		if i >= 3+4 {
			f.g.history.DeleteURLTestHistory(tag) // c04 on: no history, reached by the cursor only
		}
	}
	f.fresh("c00", "c01", "c02", "c03") // live history: polled first in the first check
	heal := &f.s.balancer.heal
	// k batches of 3 per check: 6 candidates a check, 30 in five checks.
	for check := 1; check <= 5; check++ {
		f.runCheck()
		want := int32(0)
		if check == 5 {
			want = 1
		}
		if heal.pauseStep() != want {
			t.Fatalf("check %d: step %d, want %d (polled %d)", check, heal.pauseStep(), want, heal.pollCount())
		}
	}
}

// W5: at the base step (g = 15 s) the first byte starts no round — the next one comes
// after 15 s anyway; above it (g = 30 s) one round, once.
func TestRRStand9_extraRoundOnlyAboveTheBase(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	f.s.balancer.clearSlot("a")
	answer := func() {
		f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "b", track: f.s.balancer.trackOf("b"), start: time.Now()}, nil)
	}
	end := time.Now().Add(-time.Millisecond)
	heal.setPauseEnd(&end)
	answer()
	if heal.roundBusy() || heal.extraOpen() || f.allProbes() != 0 {
		t.Fatal("g = 15 s: the first byte starts no round")
	}
	heal.setPauseStep(1)
	answer()
	waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	probes := f.allProbes()
	if probes == 0 {
		t.Fatal("g = 30 s: the first byte starts one round")
	}
	answer()
	if heal.roundBusy() || f.allProbes() != probes {
		t.Fatal("g = 30 s: one round per pause")
	}
}

// lx:end SPEC 138
