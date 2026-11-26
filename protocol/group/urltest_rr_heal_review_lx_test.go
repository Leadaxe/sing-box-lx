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
)

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
	if f.probes("x") == 0 { // the check probes the whole pool (V11)
		t.Fatalf("the check probes the leaf of the nested slot, x probed %d", f.probes("x"))
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
	h.holdRound()
	h.endRound(time.Now(), true, true, false)
	check := h.pauseEnd() // a freeze drops the gap; read it before
	h.freeze()
	h.holdRound()
	h.endRound(time.Now(), true, true, false)
	if h.pauseEnd() != nil {
		t.Fatal("a check that ends frozen sets no gap")
	}
	h.setPauseEnd(check)
	for name, mark := range map[string]*time.Time{"check end": h.pauseEnd()} {
		if mark == nil || !strings.Contains(mark.String(), " m=") {
			t.Fatalf("%s mark must keep the monotonic reading, got %v", name, mark)
		}
	}
	if !h.frozen() || h.beginRound(time.Now()) {
		t.Fatal("frozen and inside the gap right after the marks")
	}
}

// lx:end SPEC 138
