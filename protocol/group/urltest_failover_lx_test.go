// lx:begin SPEC 116 failover mode tests

package group

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// failoverNode is an outbound with a controllable probe/dial outcome: err != nil
// fails the dial, otherwise it connects to the shared test server after latency.
// dials counts every DialContext call (probes included).
type failoverNode struct {
	adapter.Outbound
	tag      string
	networks []string
	latency  time.Duration
	err      atomic.Pointer[error]
	addr     string
	dials    atomic.Int32
}

func newFailoverNode(tag string, latency time.Duration, networks ...string) *failoverNode {
	if len(networks) == 0 {
		networks = []string{N.NetworkTCP, N.NetworkUDP}
	}
	return &failoverNode{tag: tag, latency: latency, networks: networks}
}

func (n *failoverNode) Type() string           { return C.TypeDirect }
func (n *failoverNode) Tag() string            { return n.tag }
func (n *failoverNode) Network() []string      { return n.networks }
func (n *failoverNode) Dependencies() []string { return nil }
func (n *failoverNode) setErr(err error) {
	if err == nil {
		n.err.Store(nil)
		return
	}
	n.err.Store(&err)
}

func (n *failoverNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	n.dials.Add(1)
	if err := n.err.Load(); err != nil {
		return nil, *err
	}
	if n.latency > 0 {
		time.Sleep(n.latency)
	}
	return net.Dial("tcp", n.addr)
}

func newFailoverTestGroup(t *testing.T, nodes ...*failoverNode) *URLTestGroup {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	byTag := make(map[string]adapter.Outbound, len(nodes))
	outbounds := make([]adapter.Outbound, 0, len(nodes))
	for _, node := range nodes {
		node.addr = server.Listener.Addr().String()
		byTag[node.tag] = node
		outbounds = append(outbounds, node)
	}
	return &URLTestGroup{
		ctx:            context.Background(),
		outbound:       &fakeManager{byTag: byTag},
		logger:         logger.NOP(),
		outbounds:      outbounds,
		link:           server.URL,
		interval:       time.Minute,
		tolerance:      50,
		history:        urltest.NewHistoryStorage(),
		interruptGroup: interrupt.NewGroup(),
		failover:       true,
	}
}

// ageHistory makes every stored result older than interval, so the next
// non-forced run probes the nodes it is asked to (testNodes skips fresh history).
func ageHistory(g *URLTestGroup) {
	for _, detour := range g.outbounds {
		if history := g.history.LoadURLTestHistory(detour.Tag()); history != nil {
			g.history.StoreURLTestHistory(detour.Tag(), &adapter.URLTestHistory{Time: time.Now().Add(-2 * g.interval), Delay: history.Delay})
		}
	}
}

func resetDials(nodes ...*failoverNode) {
	for _, node := range nodes {
		node.dials.Store(0)
	}
}

func tick(t *testing.T, g *URLTestGroup) {
	t.Helper()
	ageHistory(g)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func assertSelected(t *testing.T, g *URLTestGroup, network string, want *failoverNode) {
	t.Helper()
	got := g.selectedOutboundTCP
	if network == N.NetworkUDP {
		got = g.selectedOutboundUDP
	}
	if got == nil || got.Tag() != want.tag {
		var gotTag string
		if got != nil {
			gotTag = got.Tag()
		}
		t.Fatalf("%s selection: want %s, got %q", network, want.tag, gotTag)
	}
}

func assertDials(t *testing.T, node *failoverNode, want int32) {
	t.Helper()
	if got := node.dials.Load(); got != want {
		t.Fatalf("node %s: want %d probes/dials, got %d", node.tag, want, got)
	}
}

// §4.7 cold start: before any result the first listed node; after the first run
// the fastest one, regardless of list order.
func TestFailover_coldStart(t *testing.T) {
	slow := newFailoverNode("slow", 60*time.Millisecond)
	fast := newFailoverNode("fast", 0)
	g := newFailoverTestGroup(t, slow, fast)

	if first := g.pickForDial(N.NetworkTCP); first == nil || first.Tag() != "slow" {
		t.Fatal("before the first result the first listed node must be used")
	}
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertSelected(t, g, N.NetworkTCP, fast)
	assertSelected(t, g, N.NetworkUDP, fast)
}

// §4.1 hold + §4.2 one probe: the held node turned slower than another live node;
// the tick probes only the held node and keeps it.
func TestFailover_holdProbesOnlyCurrent(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 60*time.Millisecond)
	c := newFailoverNode("c", 80*time.Millisecond)
	g := newFailoverTestGroup(t, a, b, c)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertSelected(t, g, N.NetworkTCP, a)

	a.latency = 100 * time.Millisecond
	b.latency = 0
	resetDials(a, b, c)
	tick(t, g)

	assertSelected(t, g, N.NetworkTCP, a)
	assertSelected(t, g, N.NetworkUDP, a)
	assertDials(t, a, 1)
	assertDials(t, b, 0)
	assertDials(t, c, 0)
}

// §4.3 move on probe failure: the held node stops answering → full run, the
// fastest live node wins; interrupt_exist_connections is honoured.
func TestFailover_moveOnProbeFailure(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 80*time.Millisecond)
	c := newFailoverNode("c", 20*time.Millisecond)
	g := newFailoverTestGroup(t, a, b, c)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertSelected(t, g, N.NetworkTCP, a)

	internal := &trackConn{}
	external := &trackConn{}
	g.interruptGroup.NewConn(internal, false)
	g.interruptGroup.NewConn(external, true)

	a.setErr(context.DeadlineExceeded)
	resetDials(a, b, c)
	tick(t, g)

	assertSelected(t, g, N.NetworkTCP, c)
	assertSelected(t, g, N.NetworkUDP, c)
	assertDials(t, a, 1) // the failed held node is not re-probed in the escalation
	if b.dials.Load() == 0 || c.dials.Load() == 0 {
		t.Fatal("escalation must probe every other node")
	}
	if !internal.closed.Load() {
		t.Fatal("a selection change must interrupt internal connections")
	}
	if external.closed.Load() {
		t.Fatal("interrupt_exist_connections=false must keep external connections")
	}

	// interrupt_exist_connections=true: the next move closes external ones too.
	g.interruptExternalConnections = true
	external2 := &trackConn{}
	g.interruptGroup.NewConn(external2, true)
	c.setErr(context.DeadlineExceeded)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, b)
	if !external2.closed.Load() {
		t.Fatal("interrupt_exist_connections=true must close external connections on a move")
	}
}

// §4.4 move on dial failure: SPEC 054 moves the selection to the fastest
// non-penalized node; the next tick probes only that node.
func TestFailover_moveOnDialFailure(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 80*time.Millisecond)
	c := newFailoverNode("c", 20*time.Millisecond)
	g := newFailoverTestGroup(t, a, b, c)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertSelected(t, g, N.NetworkTCP, a)

	a.setErr(context.DeadlineExceeded)
	g.history.DeleteURLTestHistory("a") // what DialContext does on a failed dial
	conn, fallback, ok := g.penaltyFailoverDial(context.Background(), N.NetworkTCP, M.Socksaddr{}, a, context.DeadlineExceeded)
	if !ok {
		t.Fatal("dial failover must succeed via a live node")
	}
	conn.Close()
	if fallback.Tag() != "c" {
		t.Fatalf("fallback must be the fastest non-penalized node c, got %s", fallback.Tag())
	}
	assertSelected(t, g, N.NetworkTCP, c)

	// The dial failure moved TCP only (UDP produces no penalties): UDP still holds
	// a, so the tick probes it, finds it dead and moves UDP to c as well.
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, c)
	assertSelected(t, g, N.NetworkUDP, c)

	resetDials(a, b, c)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, c)
	assertDials(t, c, 1)
	assertDials(t, a, 0)
	assertDials(t, b, 0)
}

// §4.5 no fallback to the former best: it revived, the held node is alive → no move.
func TestFailover_noReturnToFormerBest(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 40*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	a.setErr(context.DeadlineExceeded)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, b)

	a.setErr(nil)
	resetDials(a, b)
	tick(t, g)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, b)
	assertDials(t, a, 0)
	assertDials(t, b, 2)
}

// §4.6 manual test: probes every node and re-selects the fastest even though
// the held node is alive.
func TestFailover_manualTestReselects(t *testing.T) {
	a := newFailoverNode("a", 0)
	b := newFailoverNode("b", 40*time.Millisecond)
	g := newFailoverTestGroup(t, a, b)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	a.setErr(context.DeadlineExceeded)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, b)

	a.setErr(nil)
	resetDials(a, b)
	result, err := g.URLTest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("manual test must report every node, got %v", result)
	}
	assertDials(t, a, 1)
	assertDials(t, b, 1)
	assertSelected(t, g, N.NetworkTCP, a)
	assertSelected(t, g, N.NetworkUDP, a)
}

// §4.8 UDP is held separately and moves on its own failure; TCP keeps its node.
func TestFailover_udpHeldSeparately(t *testing.T) {
	tcp := newFailoverNode("t", 0, N.NetworkTCP)
	udp1 := newFailoverNode("u1", 0, N.NetworkUDP)
	udp2 := newFailoverNode("u2", 40*time.Millisecond, N.NetworkUDP)
	g := newFailoverTestGroup(t, tcp, udp1, udp2)
	if _, err := g.urlTest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertSelected(t, g, N.NetworkTCP, tcp)
	assertSelected(t, g, N.NetworkUDP, udp1)

	resetDials(tcp, udp1, udp2)
	tick(t, g)
	assertDials(t, tcp, 1)
	assertDials(t, udp1, 1)
	assertDials(t, udp2, 0)

	udp1.setErr(context.DeadlineExceeded)
	tick(t, g)
	assertSelected(t, g, N.NetworkTCP, tcp)
	assertSelected(t, g, N.NetworkUDP, udp2)

	resetDials(tcp, udp1, udp2)
	tick(t, g)
	assertDials(t, tcp, 1)
	assertDials(t, udp2, 1)
	assertDials(t, udp1, 0)
}

// §4.9 config: balancer with failover is an error; non-zero tolerance warns;
// passive_check is an unknown field.
func TestFailover_config(t *testing.T) {
	_, err := NewURLTest(context.Background(), nil, logger.NOP(), "g", option.URLTestOutboundOptions{
		Outbounds: []string{"a"},
		Mode:      C.URLTestModeFailover,
		Balancer:  &option.URLTestBalancerOptions{Pool: 2},
	})
	if err == nil {
		t.Fatal("balancer with mode: failover must fail to start")
	}

	outbound, err := NewURLTest(context.Background(), nil, logger.NOP(), "g", option.URLTestOutboundOptions{
		Outbounds: []string{"a"},
		Mode:      C.URLTestModeFailover,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode := outbound.(*URLTest).Mode(); mode != C.URLTestModeFailover {
		t.Fatalf("Mode() must report failover, got %s", mode)
	}

	options := option.URLTestOutboundOptions{Mode: C.URLTestModeFailover, Tolerance: 100}
	if !warnFailoverTolerance(isFailoverMode(options), options) {
		t.Fatal("non-zero tolerance in failover must warn")
	}
	options.Tolerance = 0
	if warnFailoverTolerance(isFailoverMode(options), options) {
		t.Fatal("zero tolerance must not warn")
	}
	options = option.URLTestOutboundOptions{Tolerance: 100}
	if warnFailoverTolerance(isFailoverMode(options), options) {
		t.Fatal("least_test must not get the failover tolerance warning")
	}

	var decoded option.URLTestOutboundOptions
	if err := json.UnmarshalContextDisallowUnknownFields(context.Background(), []byte(`{"outbounds":["a"],"passive_check":true}`), &decoded); err == nil {
		t.Fatal("passive_check must be rejected as an unknown field")
	}
}

// trackConn records Close; enough for the interrupt group.
type trackConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackConn) Close() error {
	c.closed.Store(true)
	return nil
}

// lx:end SPEC 116
