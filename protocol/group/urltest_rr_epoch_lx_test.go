// lx:begin SPEC 138 round_robin: the epoch E and the events of the network (N0, N3)

package group

import (
	"context"
	"testing"
	"time"
)

// С9: the first outcome after a sleep the monotonic clock missed — before any pick —
// compares the clocks first (N3): the event freezes the group, and the exchange that
// started before it takes part in no rule; the retry goes on.
func TestRREpoch_C9_firstOutcomeAfterSleepIsAnEvent(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	b := f.s.balancer
	now := time.Now()
	b.clockMono = rrClock(now.Add(-2 * time.Second))
	b.clockWall = now.Add(-2*time.Second - 10*time.Minute).UnixNano() // ten minutes of sleep
	conn, ok := f.failAt(context.Background(), "a", now.Add(-time.Second))
	closeConn(conn)
	if !b.heal.frozen() || b.heal.epoch() < rrClock(now) {
		t.Fatal("the outcome compared the clocks before the rules: an event")
	}
	if !ok || b.trackOf("a").pendingN.Load() != 0 || b.heal.roundBusy() {
		t.Fatal("N0: the retry, no record, no check while frozen")
	}
}

// I4: an exchange that started during a freeze and ended after it takes part in no
// rule; it still gets its retry.
func TestRREpoch_I4_startedInTheFreezeGivesNothing(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	start := time.Now()
	heal.epochEnd.Store(rrClock(start.Add(30 * time.Millisecond)))
	time.Sleep(40 * time.Millisecond)
	conn, ok := f.failAt(context.Background(), "a", start)
	closeConn(conn)
	if !ok {
		t.Fatal("after the freeze the retry is back")
	}
	if f.s.balancer.trackOf("a").pendingN.Load() != 0 || heal.roundBusy() || f.probes("a") != 0 {
		t.Fatal("an exchange begun before E records nothing and starts nothing")
	}
}

// N3: a signal during a freeze extends it from its own moment; the end only grows.
func TestRREpoch_signalExtendsTheFreeze(t *testing.T) {
	var heal rrHeal
	heal.freeze()
	first := heal.epoch()
	time.Sleep(5 * time.Millisecond)
	heal.freeze()
	if heal.epoch() <= first || !heal.frozen() {
		t.Fatal("a later signal extends the freeze")
	}
	heal.epochEnd.Store(first + int64(time.Hour))
	heal.freeze()
	if heal.epoch() != first+int64(time.Hour) {
		t.Fatal("the end of the freeze never moves back")
	}
}

// С1 / scenario 7: every node stalled, then the network comes back and a node
// answers. The answer is a proof of its node, no event: nothing freezes (N2 and N4
// are gone — the event comes from N3 only).
func TestRREpoch_C1_networkBackIsNoEvent(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		track := f.s.balancer.trackOf(tag)
		var entry rrTrackEntry
		track.track(&track.dials, &entry, rrClock(time.Now().Add(-rrHangAfter-time.Second)), nopKill{})
	}
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "a", track: f.s.balancer.trackOf("a"), start: time.Now()}, nil)
	if f.s.balancer.heal.frozen() || f.s.balancer.heal.epochEnd.Load() != 0 {
		t.Fatal("the network back is no event")
	}
}

// lx:end SPEC 138
