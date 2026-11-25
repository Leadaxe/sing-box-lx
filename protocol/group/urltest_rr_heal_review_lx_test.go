// lx:begin SPEC 138 round_robin self-heal tests: review fixes (stage 1)

package group

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

// The local network is gone and every node is silent, but B, the third and the
// candidates have fresh history: the controls are probed live all the same, so
// live A is not struck out on history alone.
func TestRRHeal_controlsProbedLiveDespiteFreshHistory(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	for _, node := range f.nodes {
		node.dead.Store(true)
	}
	f.fresh("b", "c", "d", "e")
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("own network: a must keep its history and slot, pool %v", f.pool())
	}
	if f.probes("b") < 1 || f.probes("c") != 1 { // b also got the verdict probe of its failed retry
		t.Fatalf("B and the third are controls: probed live, b=%d c=%d", f.probes("b"), f.probes("c"))
	}
	if !f.s.balancer.heal.frozen() {
		t.Fatal("nobody answered live: the group must freeze")
	}

	// No neighbour: the replacement search is the control, its candidates are
	// probed live too.
	g := newHealFixture(t, []string{"a", "d"}, []string{"a"}, 0)
	g.nodes["a"].dead.Store(true)
	g.nodes["d"].dead.Store(true)
	g.fresh("d")
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	g.waitCheck(t)
	if !samePool(g.pool(), "a") || g.g.history.LoadURLTestHistory("a") == nil || g.probes("d") < 1 {
		t.Fatalf("control candidates are probed live: pool %v, d probed %d", g.pool(), g.probes("d"))
	}

	// An ordinary replacement (A already proved dead by a live control) may take a
	// candidate by its fresh history, unprobed.
	h := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	h.nodes["a"].dead.Store(true)
	h.fresh("d")
	conn, _ = h.fail(context.Background(), "a")
	closeConn(conn)
	h.waitCheck(t)
	if !samePool(h.pool(), "d", "b", "c") || h.probes("d") != 0 {
		t.Fatalf("a fresh candidate replaces dead a unprobed: pool %v, d probed %d", h.pool(), h.probes("d"))
	}
}

// The scheduled check answers for A while the self-heal probe of A is still in
// flight and then fails: the newer answer wins, the verdict is dropped.
func TestRRHeal_strikeKeepsNewerHistory(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	a := f.nodes["a"]
	a.dead.Store(true)
	a.hold = make(chan struct{})
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	waitUntil(t, func() bool { return f.probes("a") == 1 && f.probes("b") == 1 })
	f.fresh("a") // the tick probed a alive meanwhile
	close(a.hold)
	f.waitCheck(t)
	if h := f.g.history.LoadURLTestHistory("a"); h == nil || time.Since(h.Time) > time.Second {
		t.Fatal("the newer answer of the tick must survive the verdict")
	}
	if !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a dropped verdict replaces nothing, pool %v", f.pool())
	}
}

// newHealInner is a least_test group "inner" over the given leaf, wired well
// enough to dial.
func newHealInner(f *healFixture, leaf *healNode) *URLTest {
	group := &URLTestGroup{
		ctx:            f.g.ctx,
		outbound:       f.g.outbound,
		logger:         log.NewNOPFactory().NewLogger("test"),
		outbounds:      []adapter.Outbound{leaf},
		link:           f.g.link,
		interval:       time.Minute,
		tolerance:      50,
		history:        f.g.history,
		interruptGroup: interrupt.NewGroup(),
	}
	inner := &URLTest{
		Adapter:  outbound.NewAdapter(C.TypeURLTest, "inner", []string{N.NetworkTCP}, []string{leaf.tag}),
		outbound: f.g.outbound,
		logger:   log.NewNOPFactory().NewLogger("test"),
		group:    group,
	}
	f.g.outbound.(*fakeManager).byTag["inner"] = inner
	return inner
}

// The retry does not go into a slot holding a nested urltest (it would retry on
// its own, more than two attempts per call): the next slot that is not one takes
// it, and with none there is no retry. The check still probes that slot's leaf.
func TestRRHeal_retrySkipsNestedURLTest(t *testing.T) {
	f := newHealFixture(t, []string{"a", "x", "c", "d"}, nil, 0)
	newHealInner(f, f.nodes["x"])
	f.g.balancer.setSlots([]string{"a", "inner", "c"})
	f.fresh("c")
	f.nodes["a"].dead.Store(true)
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if !ok || f.nodes["c"].dials.Load() != 1 || f.nodes["x"].dials.Load() != 0 {
		t.Fatalf("the retry must skip the nested urltest: ok=%v c=%d x=%d", ok, f.nodes["c"].dials.Load(), f.nodes["x"].dials.Load())
	}
	f.waitCheck(t)
	if f.probes("x") != 2 { // once with a, once after a's silence (the bracket)
		t.Fatalf("the control of the check is the leaf of the next slot, x probed %d", f.probes("x"))
	}

	g := newHealFixture(t, []string{"a", "x", "d"}, nil, 0)
	newHealInner(g, g.nodes["x"])
	g.g.balancer.setSlots([]string{"a", "inner"})
	g.nodes["a"].dead.Store(true)
	conn, ok = g.fail(context.Background(), "a")
	closeConn(conn)
	if ok || g.nodes["x"].dials.Load() != 0 {
		t.Fatalf("no slot but a nested urltest: no retry, ok=%v x=%d", ok, g.nodes["x"].dials.Load())
	}
	g.waitCheck(t)
}

// A freeze or a network change in the middle of the replacement search aborts it
// at once, and the full run of the network change gets the health-check flag
// instead of finding it busy.
func TestRRHeal_networkChangeAbortsSearchAndRuns(t *testing.T) {
	f := newHealFixture(t, []string{"a", "d", "e"}, []string{"a"}, 0)
	f.g.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(f.g.ctx))
	f.nodes["a"].dead.Store(true)
	d := f.nodes["d"]
	d.hold = make(chan struct{})
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	waitUntil(t, func() bool { return d.probes.Load() == 1 && f.s.balancer.heal.running.Load() })
	aProbes, eProbes := f.probes("a"), f.probes("e")
	f.s.InterfaceUpdated(context.Background())
	waitUntil(t, func() bool { return !f.s.balancer.heal.running.Load() })
	close(d.hold)
	waitUntil(t, func() bool { return f.probes("e") > eProbes && f.probes("a") > aProbes })
	if !samePool(f.pool(), "a") && !samePool(f.pool(), "d") && !samePool(f.pool(), "e") {
		t.Fatalf("unexpected pool %v", f.pool())
	}

	// A freeze (a wake) aborts the search the same way.
	g := newHealFixture(t, []string{"a", "d", "e"}, []string{"a"}, 0)
	g.nodes["a"].dead.Store(true)
	g.nodes["d"].hold = make(chan struct{})
	defer close(g.nodes["d"].hold)
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	waitUntil(t, func() bool { return g.nodes["d"].probes.Load() == 1 })
	g.s.balancer.heal.freeze()
	waitUntil(t, func() bool { return !g.s.balancer.heal.running.Load() && !g.g.checking.Load() })
	if !samePool(g.pool(), "a") || g.g.history.LoadURLTestHistory("a") == nil {
		t.Fatalf("an aborted search changes nothing, pool %v", g.pool())
	}
}

// A retry that does not open leaves the first node in the connection's cell and
// in Now(); one that opens replaces it.
func TestRRHeal_failedRetryKeepsFirstPick(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.g.groupTag = "auto"
	f.s.balancer.stickyHash = nil
	f.nodes["a"].dead.Store(true)
	f.nodes["b"].dead.Store(true)
	ctx, cell := actualContext()
	if _, err := f.s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443)); err == nil {
		t.Fatal("both attempts fail")
	}
	if got := cell.Load().Picked("auto"); got == nil || got.Tag() != "a" {
		t.Fatalf("a failed retry keeps the first node in the cell, got %v", got)
	}
	if got := f.g.lastSelected.Load(); got != "a" {
		t.Fatalf("a failed retry keeps the first node in Now(), got %q", got)
	}

	f.waitCheck(t)

	g := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	g.g.groupTag = "auto"
	g.s.balancer.stickyHash = nil
	g.nodes["a"].dead.Store(true)
	ctx, cell = actualContext()
	conn, err := g.s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal(err)
	}
	closeConn(conn)
	if got := cell.Load().Picked("auto"); got == nil || got.Tag() != "b" || g.g.lastSelected.Load() != "b" {
		t.Fatalf("an opened retry records its node, got %v / %q", got, g.g.lastSelected.Load())
	}
	g.waitCheck(t)
}

// The marks of the freeze and of the end of a check keep the monotonic clock
// reading: a wall clock step does not stretch the freeze or the probe gap.
func TestRRHeal_marksMonotonic(t *testing.T) {
	var h rrHeal
	h.freeze()
	h.running.Store(true)
	h.endCheck()
	for name, mark := range map[string]*time.Time{"freeze": h.frozenUntil.Load(), "check end": h.lastCheckEnd.Load()} {
		if mark == nil || !strings.Contains(mark.String(), " m=") {
			t.Fatalf("%s mark must keep the monotonic reading, got %v", name, mark)
		}
	}
	if !h.frozen() || h.beginCheck() {
		t.Fatal("frozen and inside the gap right after the marks")
	}
}

// lx:end SPEC 138
