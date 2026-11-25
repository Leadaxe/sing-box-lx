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
		interruptGroup: interrupt.NewGroup(),
		lxURLTestGroup: lxURLTestGroup{balancer: &balancer{poolSize: poolSize}},
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

	g.balancePoolFirstLive(context.Background(), 2, nil)

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

	g.balancePoolFirstLive(context.Background(), 2, nil)

	tags := g.balancer.poolTags()
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("expected [a b] (dead b keeps its slot), got %v", tags)
	}
}

// TestBalancePoolFirstLive_fullPoolProbesOnlyThePoolAndTheReserve: with every slot
// alive, the lazy check walks out of the pool only for its reserve (R10: ceil(2/2) =
// 1 live node), which takes no slot; the nodes past it stay untouched.
func TestBalancePoolFirstLive_fullPoolProbesOnlyThePoolAndTheReserve(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b", addr: "server"}
	c := &testDialNode{tag: "c", addr: "server"}
	d := &testDialNode{tag: "d", addr: "server"}
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b, c, d}, 2)
	g.balancer.setSlots([]string{"a", "b"})

	g.balancePoolFirstLive(context.Background(), 2, nil)

	if g.history.LoadURLTestHistory("c") == nil || g.history.LoadURLTestHistory("d") != nil {
		t.Fatal("a full live pool probes its reserve (c) and nothing past it (d)")
	}
	tags := g.balancer.poolTags()
	if tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("full live pool must stay unchanged, got %v", tags)
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

	if g.selectedOutboundTCP.Load() == nil || g.selectedOutboundTCP.Load().Tag() != "a" {
		t.Fatal("precondition: first selection must land on a")
	}
	if inv.calls == 0 {
		t.Fatal("the FIRST selection must invalidate the reachable cache (nil→first lost update)")
	}
}

// lx:end idle-suspend
