// lx:begin SPEC 138 round_robin: the event of the network, a foreign control, the
// bracket on a slow network, a pool of one, the clock jump, silence confirms nothing

package group

import (
	"context"
	"testing"
	"time"

	N "github.com/sagernet/sing/common/network"
)

// 1. Reproduction: the network goes away, a request X hangs waiting for its first
// byte through a (one node only: no freeze), the network comes back and a proves
// alive. X started before that event of the network: it gives no verdict — its key
// does not move, not at the look, not after a probe, not at its close.
func TestRREvent_connectionBeforeTheEventGivesNoVerdict(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	x, inner := f.openHole(t, key, "hole.example")
	track := f.s.balancer.trackOf("a")
	agedWait(track, inner)
	heal := &f.s.balancer.heal
	heal.lastByte.Store(rrClock(time.Now().Add(-rrHangAfter - 2*time.Second)))
	heal.lastSilence.Store(rrClock(time.Now().Add(-time.Second))) // a probe went silent in the outage
	// The network is back: a new connection through a gets its first byte.
	if _, answered := f.dial(t, f.keyOn(t, "ok.example", 0), "ok.example"); !answered {
		t.Fatal("precondition: a serves again")
	}
	if heal.frozen() {
		t.Fatal("one stuck node: no freeze")
	}
	if heal.event() < rrClock(inner.watch.start) {
		t.Fatal("the end of the silence is an event of the network")
	}
	f.s.selectBalanced(f.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example"))
	track.hangLook.Store(0)
	f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
	f.s.rrSettleProbe(rrProbe{member: "a", leaf: "a", answered: true, started: time.Now(), ended: time.Now()})
	x.Close()
	if shift, _ := f.shift(key, "hole.example"); shift != 0 {
		t.Fatalf("a connection from before the event moves no key, got %d", shift)
	}
	if track.silentSince.Load() != 0 || track.pendingN.Load() != 0 || f.fails("a") != 0 {
		t.Fatal("and leaves no suspicion, no pending failure, no count")
	}
}

// 3. Reproduction: the pool [a, b] sits on one fallen server; both stall; the check
// probes a and b — silent — and the candidate c outside the pool answers. c was not
// stuck: its answer is no "network back", no freeze; a and b are struck out.
func TestRRStall_foreignControlIsNoNetworkBack(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b"})
	never := make(chan struct{})
	defer close(never)
	for _, tag := range []string{"a", "b"} {
		node := f.fb[tag]
		node.hold = never
		track := f.s.balancer.trackOf(tag)
		var entry rrTrackEntry
		track.track(&track.dials, &entry, rrClock(time.Now().Add(-rrHangAfter-time.Second)), nopKill{})
	}
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now().Add(-rrHangAfter - 2*time.Second)))
	if !f.s.rrStartCheck(f.fb["a"], false) {
		t.Fatal("precondition: the check starts")
	}
	f.waitCheck(t)
	if f.s.balancer.heal.frozen() {
		t.Fatal("the answer of a node that was not stuck is no network back")
	}
	if f.g.history.LoadURLTestHistory("a") != nil || f.g.history.LoadURLTestHistory("b") != nil {
		t.Fatal("a and b are struck out")
	}
}

// 4. Reproduction: a slow network — the neighbour answers after half the window, a
// is dead. No freeze on the late answer: a second round of the control and a
// strikes a out.
func TestRRBracket_slowNetworkStrikesDeadNode(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	never := make(chan struct{})
	defer close(never)
	f.fb["a"].hold = never
	f.fb["b"].slow = 250 * time.Millisecond
	f.s.rrStartCheck(f.fb["a"], false)
	f.waitCheck(t)
	if f.s.balancer.heal.frozen() {
		t.Fatal("a late answer of the control alone is no own network")
	}
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("dead a is struck out after the second round")
	}
	if f.probes("a") != 2 || f.probes("b") != 2 {
		t.Fatalf("the second round probes a and b together: a %d b %d", f.probes("a"), f.probes("b"))
	}
}

// 5. Reproduction: a pool of one; a and the candidate c are silent — the own
// network: freeze, and no replacement search after a's window (when the network is
// back by then, it would strike live a out).
func TestRRBracket_poolOfOneOwnNetwork(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "c"}, []string{"a"})
	down := make(chan struct{})
	f.fb["a"].hold, f.fb["c"].hold = down, down
	time.AfterFunc(rrProbeTimeout()+20*time.Millisecond, func() { close(down) })
	f.s.rrStartCheck(f.fb["a"], false)
	f.waitCheck(t)
	if !f.s.balancer.heal.frozen() {
		t.Fatal("a and the control silent: the own network")
	}
	if f.probes("c") != 1 || !samePool(f.pool(), "a") || f.g.history.LoadURLTestHistory("a") == nil {
		t.Fatalf("no replacement search after the window: c probed %d, pool %v", f.probes("c"), f.pool())
	}
}

// 6. A wait closed by the client during a freeze leaves no sticky suspicion.
func TestRRSilence_noStickyMarkWhileFrozen(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	_, inner := f.openHole(t, f.keyOn(t, "hole.example", 0), "hole.example")
	inner.wroteAt.Add(-int64(rrHangAfter + time.Second))
	f.s.balancer.heal.freeze()
	f.s.balancer.heal.lastFreeze.Store(rrClock(inner.watch.start) - 1) // the freeze, not the event, is under test
	inner.Close()
	if f.s.balancer.trackOf("a").silentSince.Load() != 0 {
		t.Fatal("no sticky suspicion while frozen")
	}
}

// 7. a dead by timeout, b answers at once: the third node is not probed.
func TestRRBracket_noThirdWhenNeighbourAnswered(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	never := make(chan struct{})
	defer close(never)
	f.fb["a"].hold = never
	f.s.rrStartCheck(f.fb["a"], false)
	f.waitCheck(t)
	if f.probes("c") != 0 {
		t.Fatalf("b answered in time: c is not probed, got %d", f.probes("c"))
	}
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("a is struck out")
	}
}

// 8. The wall clock ran far ahead of the monotonic one between two looks of the
// pick (a sleep that no wake signal reported): an event of the network and a
// freeze.
func TestRRClockJump_sleepFreezes(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	b := f.s.balancer
	now := time.Now()
	b.clockMono.Store(rrClock(now.Add(-2 * time.Second)))
	b.clockWall.Store(now.Add(-2*time.Second - time.Minute).UnixNano()) // a minute of sleep
	f.s.selectBalanced(f.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example"))
	if !b.heal.frozen() || b.heal.event() < rrClock(now.Add(-time.Second)) {
		t.Fatal("a sleep the monotonic clock missed is an event of the network and a freeze")
	}
	g := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	g.s.balancer.clockMono.Store(rrClock(now.Add(-2 * time.Second)))
	g.s.balancer.clockWall.Store(now.Add(-2 * time.Second).UnixNano())
	g.s.selectBalanced(g.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example"))
	if g.s.balancer.heal.frozen() {
		t.Fatal("the clocks in step: nothing")
	}
}

// 9. A key that passed a node by silence (a silent site through a live node)
// confirms no failure to it when the key gets its first byte elsewhere.
func TestRRSiteSilence_confirmsNoFailure(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	hole, inner := f.openHole(t, key, "hole.example")
	if _, answered := f.dial(t, f.keyOn(t, "ok.example", 0), "ok.example"); !answered {
		t.Fatal("precondition: a serves")
	}
	agedWait(f.s.balancer.trackOf("a"), inner)
	hole.Close() // the client gives up
	if shift, _ := f.shift(key, "hole.example"); shift != 1 {
		t.Fatal("precondition: the key moved on by silence")
	}
	f.fb["b"].set(&f.fb["b"].mute, "hole.example", false)
	if _, answered := f.dial(t, key, "hole.example"); !answered {
		t.Fatal("the key answers through b")
	}
	if f.fails("a") != 0 {
		t.Fatalf("silence confirms no failure to a, got %d", f.fails("a"))
	}
}

// lx:end SPEC 138
