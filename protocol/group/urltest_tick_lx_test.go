// lx:begin SPEC 125 tick freshness tests

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// shiftTime moves the previous tick run end and all history back by d, as if d
// had passed since the last run.
func shiftTime(g *URLTestGroup, d time.Duration) {
	if nanos := g.lastTickRunEnd.Load(); nanos != 0 {
		g.lastTickRunEnd.Store(nanos - int64(d))
	}
	for _, detour := range g.outbounds {
		if history := g.history.LoadURLTestHistory(detour.Tag()); history != nil {
			g.history.StoreURLTestHistory(detour.Tag(), &adapter.URLTestHistory{Time: history.Time.Add(-d), Delay: history.Delay})
		}
	}
}

func runTick(t *testing.T, g *URLTestGroup) {
	t.Helper()
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

// least_test: history written by the previous tick run never skips the next tick,
// however little time has passed.
func TestTick_leastTestProbesEveryTick(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 20*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	g.failover = false
	runTick(t, g)
	shiftTime(g, g.interval-time.Second)
	resetDials(a, b)
	runTick(t, g)
	assertDials(t, a, 1)
	assertDials(t, b, 1)
}

// failover: the held node is probed on every tick; the others are not.
func TestTick_failoverHeldProbedEveryTick(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 60*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	runTick(t, g)
	assertSelected(t, g, N.NetworkTCP, a)
	shiftTime(g, g.interval-time.Second)
	resetDials(a, b)
	runTick(t, g)
	assertDials(t, a, 1)
	assertDials(t, b, 0)
}

// A probe after the previous tick run (manual URLTest) stays fresh for the next tick.
func TestTick_manualRunStaysFresh(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 20*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	g.failover = false
	runTick(t, g)
	shiftTime(g, g.interval-time.Second)
	if _, err := g.urlTest(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	resetDials(a, b)
	runTick(t, g)
	assertDials(t, a, 0)
	assertDials(t, b, 0)
}

// Long after the previous tick run the window is capped at interval (upstream).
func TestTick_windowCappedAtInterval(t *testing.T) {
	a := newFailoverNode("a", 0)
	g := newFailoverTestGroup(t, a)
	g.failover = false
	g.lastTickRunEnd.Store(time.Now().Add(-3 * g.interval).UnixNano())
	g.history.StoreURLTestHistory("a", &adapter.URLTestHistory{Time: time.Now().Add(-g.interval / 2), Delay: 10})
	resetDials(a)
	runTick(t, g)
	assertDials(t, a, 0)
}

// A tick less than interval/2 after the previous tick run ended (the tick the
// ticker buffered during a long run) is skipped; manual runs are not.
func TestTick_quietPauseAfterLongRun(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 20*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	g.failover = false
	runTick(t, g)
	resetDials(a, b)
	runTick(t, g)
	assertDials(t, a, 0)
	assertDials(t, b, 0)

	shiftTime(g, g.interval/2-time.Second)
	runTick(t, g)
	assertDials(t, a, 0)

	shiftTime(g, 2*time.Second)
	runTick(t, g)
	assertDials(t, a, 1)
	assertDials(t, b, 1)

	resetDials(a, b)
	if _, err := g.urlTest(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	assertDials(t, a, 1)
}

func TestTick_freshWindow(t *testing.T) {
	now := time.Now()
	interval := 3 * time.Minute
	for _, c := range []struct {
		lastEnd time.Time
		want    time.Duration
	}{
		{time.Time{}, interval},
		{now.Add(-interval + 5*time.Second), interval - 5*time.Second},
		{now.Add(-2 * interval), interval},
		{now.Add(time.Second), 0},
	} {
		if got := tickFreshWindow(interval, c.lastEnd, now); got != c.want {
			t.Fatalf("lastEnd %v: want %v, got %v", now.Sub(c.lastEnd), c.want, got)
		}
	}
}

// lx:end SPEC 125
