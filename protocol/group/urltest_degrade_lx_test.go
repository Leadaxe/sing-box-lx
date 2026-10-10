// lx:begin SPEC 126 failover degradation tests

package group

import (
	"context"
	"testing"
	"time"

	N "github.com/sagernet/sing/common/network"
)

func startFailover(t *testing.T, nodes ...*failoverNode) *URLTestGroup {
	t.Helper()
	g := newFailoverTestGroup(t, nodes...)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	return g
}

// Held node turned more than 2× slower than the best other one: confirmed by a
// second probe, full run, move to a node more than 1.5× faster.
func TestDegrade_moveWhenConfirmed(t *testing.T) {
	a := newFailoverNode("a", 10*time.Millisecond)
	b := newFailoverNode("b", 100*time.Millisecond)
	c := newFailoverNode("c", 200*time.Millisecond)
	g := startFailover(t, a, b, c)
	assertSelected(t, g, N.NetworkTCP, a)

	a.latency = 800 * time.Millisecond
	resetDials(a, b, c)
	tick(t, g)
	assertDials(t, a, 2)
	assertDials(t, b, 1)
	assertDials(t, c, 1)
	assertSelected(t, g, N.NetworkTCP, b)
	assertSelected(t, g, N.NetworkUDP, b)
}

// Slower, but not 2× slower than the best other node: no trigger, one probe.
func TestDegrade_noTriggerBelowTwice(t *testing.T) {
	a := newFailoverNode("a", 20*time.Millisecond)
	b := newFailoverNode("b", 200*time.Millisecond)
	g := startFailover(t, a, b)
	assertSelected(t, g, N.NetworkTCP, a)

	a.latency = 250 * time.Millisecond
	resetDials(a, b)
	tick(t, g)
	assertDials(t, a, 1)
	assertDials(t, b, 0)
	assertSelected(t, g, N.NetworkTCP, a)
}

// A one-off slow probe is filtered by the confirmation probe: no full run.
func TestDegrade_spikeFiltered(t *testing.T) {
	a := newFailoverNode("a", 10*time.Millisecond)
	b := newFailoverNode("b", 200*time.Millisecond)
	g := startFailover(t, a, b)
	assertSelected(t, g, N.NetworkTCP, a)

	a.spike.Store(int64(800 * time.Millisecond))
	resetDials(a, b)
	tick(t, g)
	assertDials(t, a, 2)
	assertDials(t, b, 0)
	assertSelected(t, g, N.NetworkTCP, a)
}

// Everyone got slower (the network, not the node): the trigger fires on the
// others' stale delays, the full run finds nobody 1.5× faster, the hold stays,
// and with fresh delays the next tick does not trigger again.
func TestDegrade_stayWhenNobodyMuchFaster(t *testing.T) {
	a := newFailoverNode("a", 10*time.Millisecond)
	b := newFailoverNode("b", 100*time.Millisecond)
	g := startFailover(t, a, b)
	assertSelected(t, g, N.NetworkTCP, a)

	a.latency = 300 * time.Millisecond
	b.latency = 250 * time.Millisecond
	resetDials(a, b)
	tick(t, g)
	assertDials(t, a, 2)
	assertDials(t, b, 1)
	assertSelected(t, g, N.NetworkTCP, a)

	resetDials(a, b)
	tick(t, g)
	assertDials(t, a, 1)
	assertDials(t, b, 0)
	assertSelected(t, g, N.NetworkTCP, a)
}

func TestDegrade_ratios(t *testing.T) {
	for _, c := range []struct {
		delay, than uint16
		num, den    int
		want        bool
	}{
		{201, 100, failoverTriggerNum, failoverTriggerDen, true},
		{200, 100, failoverTriggerNum, failoverTriggerDen, false},
		{151, 100, failoverMoveNum, failoverMoveDen, true},
		{150, 100, failoverMoveNum, failoverMoveDen, false},
		{65535, 65535, failoverTriggerNum, failoverTriggerDen, false},
	} {
		if got := slowerBy(c.delay, c.than, c.num, c.den); got != c.want {
			t.Fatalf("slowerBy(%d, %d, %d/%d): want %v", c.delay, c.than, c.num, c.den, c.want)
		}
	}
}

// lx:end SPEC 126
