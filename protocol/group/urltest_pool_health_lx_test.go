// lx:begin idle-suspend
// Direct coverage for the live round_robin health-check path (balancePoolFirstLive
// — the site of the rc.15 slot-shift bug, previously only pinned via the pure
// planner twins) and for the SPEC 020 first-selection invalidation seam.

package group

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// fakeManager resolves tags from a fixed map; just enough for URLTestGroup to
// look up its members in these tests.
type fakeManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *fakeManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, ok := m.byTag[tag]
	return ob, ok
}

// testDialNode is an adapter.Outbound whose DialContext either connects to a
// local HTTP server (alive) or fails (dead). Just enough for testNodes/Select.
type testDialNode struct {
	adapter.Outbound
	tag  string
	addr string // "" → dead
}

func (n *testDialNode) Type() string           { return C.TypeDirect }
func (n *testDialNode) Tag() string            { return n.tag }
func (n *testDialNode) Network() []string      { return []string{N.NetworkTCP} }
func (n *testDialNode) Dependencies() []string { return nil }
func (n *testDialNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if n.addr == "" {
		return nil, E.New("dead node")
	}
	return net.Dial("tcp", n.addr)
}

func newPoolTestGroup(t *testing.T, ctx context.Context, nodes []*testDialNode, poolSize int) *URLTestGroup {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	byTag := make(map[string]adapter.Outbound, len(nodes))
	outbounds := make([]adapter.Outbound, 0, len(nodes))
	for _, node := range nodes {
		if node.addr == "server" {
			node.addr = server.Listener.Addr().String()
		}
		byTag[node.tag] = node
		outbounds = append(outbounds, node)
	}
	return &URLTestGroup{
		ctx:            ctx,
		outbound:       &fakeManager{byTag: byTag},
		logger:         logger.NOP(),
		outbounds:      outbounds,
		link:           server.URL,
		interval:       time.Minute,
		history:        urltest.NewHistoryStorage(),
		balancer:       &balancer{poolSize: poolSize},
		interruptGroup: interrupt.NewGroup(),
	}
}

// TestBalancePoolFirstLive_replaceInSlot: dead slot member is replaced IN ITS
// OWN slot by the first live non-pool node; the surviving member keeps its index.
func TestBalancePoolFirstLive_replaceInSlot(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b"} // dead
	c := &testDialNode{tag: "c", addr: "server"}
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b, c}, 2)
	g.balancer.setSlots([]string{"a", "b"})

	g.balancePoolFirstLive(context.Background(), 2)

	tags := g.balancer.poolTags()
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "c" {
		t.Fatalf("expected [a c] (b replaced in slot 1, a pinned to slot 0), got %v", tags)
	}
}

// TestBalancePoolFirstLive_deadKeepsSlotWithoutReplacement: no live candidate →
// the dead member keeps its slot (pool never shrinks).
func TestBalancePoolFirstLive_deadKeepsSlotWithoutReplacement(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b"} // dead
	c := &testDialNode{tag: "c"} // dead too
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b, c}, 2)
	g.balancer.setSlots([]string{"a", "b"})

	g.balancePoolFirstLive(context.Background(), 2)

	tags := g.balancer.poolTags()
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("expected [a b] (dead b keeps its slot), got %v", tags)
	}
}

// TestBalancePoolFirstLive_fullPoolProbesOnlyPool: with every slot alive, the
// lazy check must not touch nodes outside the pool (energy: no out-of-pool
// probe traffic, no wake of suspended out-of-pool endpoints).
func TestBalancePoolFirstLive_fullPoolProbesOnlyPool(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b", addr: "server"}
	c := &testDialNode{tag: "c", addr: "server"}
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b, c}, 2)
	g.balancer.setSlots([]string{"a", "b"})

	g.balancePoolFirstLive(context.Background(), 2)

	if got := g.history.LoadURLTestHistory("c"); got != nil {
		t.Fatal("a full live pool must not probe out-of-pool nodes")
	}
	tags := g.balancer.poolTags()
	if tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("full live pool must stay unchanged, got %v", tags)
	}
}

// --- SPEC 019 passive_check ---------------------------------------------------

// TestBalancePoolFirstLive_passiveConfirmedSlotNotProbed: a slot with a fresh
// passive liveness signal (successful TCP dial through it) keeps its slot
// WITHOUT being URL-probed; a slot without the signal is probed as usual.
func TestBalancePoolFirstLive_passiveConfirmedSlotNotProbed(t *testing.T) {
	a := &testDialNode{tag: "a"} // dead to probes — but passively confirmed
	b := &testDialNode{tag: "b", addr: "server"}
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b}, 2)
	g.passiveCheck = true
	g.balancer.setSlots([]string{"a", "b"})
	g.markPassiveAlive("a")

	g.balancePoolFirstLive(context.Background(), 2)

	tags := g.balancer.poolTags()
	if tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("passively confirmed a must keep slot 0 unprobed, got %v", tags)
	}
	if got := g.history.LoadURLTestHistory("a"); got != nil {
		t.Fatal("a passively confirmed slot must not be URL-probed")
	}
	if got := g.history.LoadURLTestHistory("b"); got == nil {
		t.Fatal("a slot without a passive signal must still be probed")
	}
}

// TestPassiveFresh_offAndStale: the signal only counts with the option on and
// within the probe interval.
func TestPassiveFresh_offAndStale(t *testing.T) {
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{{tag: "a", addr: "server"}}, 1)
	g.markPassiveAlive("a")
	if g.passiveFresh("a") {
		t.Fatal("passive signal must not count with passive_check off")
	}
	g.passiveCheck = true
	if !g.passiveFresh("a") {
		t.Fatal("fresh signal with passive_check on must count")
	}
	g.passiveOK.Store("a", time.Now().Add(-2*g.interval).UnixNano())
	if g.passiveFresh("a") {
		t.Fatal("a stale signal (older than interval) must not count")
	}
}

// TestLeastTest_passiveConfirmedSkipsCycle: while the selected node is passively
// confirmed, the periodic (non-force) cycle must not probe anyone.
func TestLeastTest_passiveConfirmedSkipsCycle(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b", addr: "server"}
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b}, 0)
	g.balancer = nil // legacy least_test
	g.passiveCheck = true
	g.selectedOutboundTCP = a
	g.markPassiveAlive("a")

	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if g.history.LoadURLTestHistory("a") != nil || g.history.LoadURLTestHistory("b") != nil {
		t.Fatal("a passively confirmed selection must skip the whole probe cycle")
	}

	// Signal lapsed → the cycle probes again.
	g.passiveOK.Store("a", time.Now().Add(-2*g.interval).UnixNano())
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if g.history.LoadURLTestHistory("b") == nil {
		t.Fatal("after the passive signal lapses the cycle must probe members again")
	}
}

// --- SPEC 020: first legacy selection must invalidate the reachable cache ----

type recordingInvalidator struct {
	calls int
}

func (r *recordingInvalidator) InvalidateReachability() { r.calls++ }

// TestPerformUpdateCheck_firstSelectionInvalidates pins the nil→first fix: the
// very first auto-selection re-shapes the active tree and must dirty the cache,
// even though there is no previous selection to interrupt.
func TestPerformUpdateCheck_firstSelectionInvalidates(t *testing.T) {
	inv := &recordingInvalidator{}
	ctx := service.ContextWith[adapter.ReachabilityInvalidator](context.Background(), inv)
	a := &testDialNode{tag: "a", addr: "server"}
	g := newPoolTestGroup(t, ctx, []*testDialNode{a}, 0)
	g.balancer = nil // legacy least_test path
	g.history.StoreURLTestHistory("a", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})

	g.performUpdateCheck()

	if g.selectedOutboundTCP == nil || g.selectedOutboundTCP.Tag() != "a" {
		t.Fatal("precondition: first selection must land on a")
	}
	if inv.calls == 0 {
		t.Fatal("the FIRST selection must invalidate the reachable cache (nil→first lost update)")
	}
}

// lx:end idle-suspend
