// lx:begin SPEC 138 round_robin slot under suspicion, verdict probe, quarantine floor tests

package group

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

// hold keeps the check from a reason and the verdict probe of a node from running:
// the gap after a check from a reason, and a verdict probe just now.
func (f *shiftFixture) hold(tag string) {
	now := time.Now()
	f.s.balancer.heal.lastHangEnd.Store(&now)
	f.s.balancer.trackOf(tag).verdictLast.Store(rrClock(now))
}

// A slot with a dial in flight longer than 5 s and no proof of its node since is
// under suspicion: the pick skips it like a dead one — no history, no shift, no
// count changes. When every other slot is dead the pick takes it as before.
func TestRRSuspect_hungSlotSkipped(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].stall, "site.example", true)
	key := f.keyOn(t, "site.example", 0)
	ctx, cancel := context.WithCancel(key)
	defer cancel()
	done := f.dialAsync(ctx, "site.example")
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example")); selected.Tag() != "a" {
		t.Fatal("a dial in flight for less than 5 s is no suspicion")
	}
	ageTrack(track, rrHangAfter+time.Second)
	f.hold("a")
	for i := 0; i < 3; i++ {
		if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example")); selected.Tag() == "a" {
			t.Fatal("a slot under suspicion is skipped")
		}
	}
	if f.g.history.LoadURLTestHistory("a") == nil || f.s.balancer.memory.shifts.size.Load() != 0 || f.fails("a") != 0 {
		t.Fatal("a suspicion changes no history, shifts no key, counts nothing")
	}
	f.g.history.DeleteURLTestHistory("b")
	f.g.history.DeleteURLTestHistory("c")
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example")); selected.Tag() != "a" {
		t.Fatal("every other slot dead: the pick takes the suspect slot as before")
	}
	cancel()
	<-done
	if track.suspect(rrClock(time.Now())) {
		t.Fatal("the suspicion lifts when the connection ends")
	}
}

// A long request through an idle node (a long-poll): the slot is skipped for a
// while, the check probes the node, it answers — the suspicion lifts, the node is
// not struck, the request is not closed. Through a busy node there is no suspicion.
func TestRRSuspect_longPollIdleAndBusy(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "poll.example", true)
	key := f.keyOn(t, "poll.example", 0)
	poll, err := f.s.DialContext(key, N.NetworkTCP, siteAddr("poll.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer poll.Close()
	poll.Write([]byte("GET /poll"))
	track := f.s.balancer.trackOf("a")
	ageTrack(track, rrHangAfter+time.Second)
	if !track.suspect(rrClock(time.Now())) {
		t.Fatal("precondition: idle a is under suspicion")
	}
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("poll.example")); selected.Tag() == "a" {
		t.Fatal("the suspect slot is skipped")
	}
	f.waitCheck(t)
	if f.probes("a") == 0 {
		t.Fatal("the look at the slot probes a")
	}
	if track.suspect(rrClock(time.Now())) {
		t.Fatal("a answered: the suspicion lifts")
	}
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a answered: not struck, pool %v", f.pool())
	}
	if inner, _ := common.Cast[*rrFirstByteConn](poll); inner == nil || inner.state.Load()&rrStateClosed != 0 {
		t.Fatal("the long request is not closed")
	}
	// The node is fine again: a key of slot a goes to a. (The long request's own key
	// moved on: through a live node it waited for nothing — rrSiteSilence.)
	if selected, _ := f.s.selectBalanced(f.keyOn(t, "other.example", 0), N.NetworkTCP, siteAddr("other.example")); selected.Tag() != "a" {
		t.Fatal("a key of slot a is back on a")
	}

	// Busy: a connection started after the long request got its first byte.
	g := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	g.fb["a"].set(&g.fb["a"].mute, "poll.example", true)
	key = g.keyOn(t, "poll.example", 0)
	poll, err = g.s.DialContext(key, N.NetworkTCP, siteAddr("poll.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer poll.Close()
	poll.Write([]byte("GET /poll"))
	if _, answered := g.dial(t, g.keyOn(t, "ok.example", 0), "ok.example"); !answered {
		t.Fatal("precondition: busy a answers")
	}
	ageTrack(g.s.balancer.trackOf("a"), rrHangAfter+time.Second) // the request waits on, the other answered after it began
	if g.s.balancer.trackOf("a").suspect(rrClock(time.Now())) {
		t.Fatal("a busy node is under no suspicion")
	}
	if selected, _ := g.s.selectBalanced(key, N.NetworkTCP, siteAddr("poll.example")); selected.Tag() != "a" {
		t.Fatal("the busy slot is taken")
	}
	time.Sleep(20 * time.Millisecond)
	if g.probes("a") != 0 || g.s.balancer.heal.running.Load() {
		t.Fatal("no probe through a busy node")
	}
}

// A close without a byte through a live node and nothing else going on: a verdict
// probe of that node alone settles it in a moment, the key shifts; it takes
// neither the check slot nor its gap.
func TestRRVerdict_settlesAtOnceOutsideTheCheck(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	f.s.balancer.heal.running.Store(true) // a check of another node is in flight
	key := f.keyOn(t, "blocked.example", 0)
	f.dial(t, key, "blocked.example")
	waitUntil(t, func() bool {
		shift, _ := f.shift(key, "blocked.example")
		return shift == 1
	})
	if f.probes("a") != 1 || f.probes("b") != 0 {
		t.Fatalf("a alone is probed: a %d b %d", f.probes("a"), f.probes("b"))
	}
	f.s.balancer.heal.running.Store(false)
	if f.s.balancer.heal.lastCheckEnd.Load() != nil {
		t.Fatal("the verdict probe sets no gap")
	}
	if history := f.g.history.LoadURLTestHistory("a"); history == nil || time.Since(history.Time) > time.Second {
		t.Fatal("the answer is written to history")
	}
}

// The verdict probe is silent: nothing is decided by it, the check with a control
// runs and strikes the dead node. A failed open through a direct leaf goes the same
// way; one through a proxy goes to the check at once.
func TestRRVerdict_silentHandsOverToTheCheck(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	for _, node := range f.nodes {
		node.direct = true
	}
	f.nodes["a"].dead.Store(true)
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	f.waitCheck(t)
	if f.probes("a") != 2 || f.g.history.LoadURLTestHistory("a") != nil || !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("direct: the verdict probe, then the check strikes a: a probed %d, pool %v", f.probes("a"), f.pool())
	}
	g := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	g.nodes["a"].dead.Store(true)
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	g.waitCheck(t)
	if g.probes("a") != 1 || !samePool(g.pool(), "d", "b", "c") {
		t.Fatalf("proxy: the check at once, a probed %d, pool %v", g.probes("a"), g.pool())
	}
}

// Limits: one verdict probe in flight per node, one per node every rrVerdictGap,
// none while frozen.
func TestRRVerdict_limits(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	node, track := f.fb["a"], f.s.balancer.trackOf("a")
	hold := make(chan struct{})
	node.hold = hold
	if !f.s.rrVerdictProbe(node, track, nil) {
		t.Fatal("the first verdict probe starts")
	}
	track.verdictLast.Store(0)
	if f.s.rrVerdictProbe(node, track, nil) {
		t.Fatal("one in flight per node")
	}
	close(hold)
	waitUntil(t, func() bool { return f.s.balancer.heal.verdicts.Load() == 0 })
	node.hold = nil
	track.verdictLast.Store(rrClock(time.Now()))
	if f.s.rrVerdictProbe(node, track, nil) {
		t.Fatal("one per node every rrVerdictGap")
	}
	track.verdictLast.Store(0)
	f.s.balancer.heal.freeze()
	if f.s.rrVerdictProbe(node, track, nil) {
		t.Fatal("none while frozen")
	}
}

// Quarantine needs a failure share of at least 5%: with no failures at the
// neighbours the median is 0, and 3 of 250 is no reason.
func TestRRQuarantine_shareFloor(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 250; i++ {
			f.s.balancer.rrCountConn(tag, &f.s.balancer.heal)
		}
	}
	for i := 0; i < 3; i++ {
		f.s.rrCreditFail("a")
	}
	if f.s.balancer.isQuarantined("a") {
		t.Fatal("share 0.012 is below the floor")
	}
	g := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 50; i++ {
			g.s.balancer.rrCountConn(tag, &g.s.balancer.heal)
		}
	}
	for i := 0; i < 3; i++ {
		g.s.rrCreditFail("a")
	}
	if !g.s.balancer.isQuarantined("a") {
		t.Fatal("share 0.06 above the floor and twice the median: quarantine")
	}
}

// The log tells a client that gave up from a connection found still waiting.
func TestRRHang_logWording(t *testing.T) {
	for _, gaveUp := range []bool{true, false} {
		f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
		logger := &captureLogger{ContextLogger: f.s.logger}
		f.s.logger = logger
		if !f.s.rrHang("a", time.Now().Add(-rrHangAfter-time.Second), gaveUp) {
			t.Fatal("precondition: the check starts")
		}
		f.waitCheck(t)
		logger.access.Lock()
		text := strings.Join(logger.lines, "\n")
		logger.access.Unlock()
		if strings.Contains(text, "gave up") != gaveUp || strings.Contains(text, "has been waiting for") == gaveUp {
			t.Fatalf("gaveUp=%v: %q", gaveUp, text)
		}
	}
}

var _ adapter.Outbound = (*fbNode)(nil)

// lx:end SPEC 138
