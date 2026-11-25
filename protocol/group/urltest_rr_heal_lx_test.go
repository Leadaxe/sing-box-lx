// lx:begin SPEC 138 round_robin self-heal tests (stage 1)

package group

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

// healNode is a fake node. A probe is a dial to the group's test server; any
// other destination is a production connection, served by the same server.
//   - dead: probes and connections fail;
//   - refused: probes and connections go to a closed port (RST);
//   - siteDown: probes answer, connections fail;
//   - hold: probes block until the channel is closed;
//   - slow: probes take this long before they answer or fail;
//   - probeFails: so many next probes fail at once (an EOF), then they answer;
//   - dialErr: connections fail with this error.
//
// A probe that reaches the server goes through probeServer, in memory: real
// sockets for every probe of a -count=50 run under -race run out of ephemeral
// ports (TIME_WAIT) and fail probes at once, which no rule under test expects.
type healNode struct {
	adapter.Outbound
	tag         string
	server      string
	probeServer *pipeListener
	refused     string
	dead        atomic.Bool
	siteDown    atomic.Bool
	hold        chan struct{}
	slow        time.Duration
	probes      atomic.Int32
	dials       atomic.Int32
	direct      bool
	probeFails  atomic.Int32
	dialErr     error
	active      atomic.Int32 // probes in flight through the node
}

// Type is a proxy by default (its failed open puts the node itself under
// suspicion); direct makes it a direct outbound.
func (n *healNode) Type() string {
	if n.direct {
		return C.TypeDirect
	}
	return C.TypeSOCKS
}
func (n *healNode) Tag() string            { return n.tag }
func (n *healNode) Network() []string      { return []string{N.NetworkTCP} }
func (n *healNode) Dependencies() []string { return nil }

func (n *healNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	probe := destination.String() == n.server
	if probe {
		n.probes.Add(1)
		n.active.Add(1)
		defer n.active.Add(-1)
		if n.hold != nil {
			select {
			case <-n.hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if n.slow > 0 {
			time.Sleep(n.slow)
		}
		if n.probeFails.Add(-1) >= 0 {
			return nil, io.EOF
		}
	} else {
		n.dials.Add(1)
		if n.dialErr != nil {
			return nil, n.dialErr
		}
	}
	if n.refused != "" {
		return net.Dial("tcp", n.refused)
	}
	if n.dead.Load() || (!probe && n.siteDown.Load()) {
		return nil, E.New("node ", n.tag, " down")
	}
	if probe && n.probeServer != nil {
		return n.probeServer.dial(ctx)
	}
	return net.Dial("tcp", n.server)
}

// pipeListener is an in-memory listener: dial hands the server one end of a
// net.Pipe.
type pipeListener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// newPipeServer serves handler in memory under the address of a real server.
func newPipeServer(t testing.TB, addr net.Addr, handler http.Handler) *pipeListener {
	l := &pipeListener{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: handler}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	return l
}

func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		err := net.ErrClosed
		client.Close()
		server.Close()
		return nil, err
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return l.addr }

type healFixture struct {
	s     *URLTest
	g     *URLTestGroup
	nodes map[string]*healNode
}

// newHealFixture builds a round_robin group over the tags with the given pool. Every
// node starts alive with stale history (probed when checked); pool_tolerance as given.
func newHealFixture(t testing.TB, tags []string, pool []string, poolTolerance uint16) *healFixture {
	t.Helper()
	answer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(answer)
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	probeServer := newPipeServer(t, server.Listener.Addr(), answer)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	byTag := make(map[string]adapter.Outbound, len(tags))
	nodes := make(map[string]*healNode, len(tags))
	outbounds := make([]adapter.Outbound, 0, len(tags))
	for _, tag := range tags {
		node := &healNode{tag: tag, server: address, probeServer: probeServer}
		nodes[tag] = node
		byTag[tag] = node
		outbounds = append(outbounds, node)
	}
	g := &URLTestGroup{
		ctx:            ctx,
		outbound:       &fakeManager{byTag: byTag},
		logger:         log.NewNOPFactory().NewLogger("test"),
		outbounds:      outbounds,
		link:           server.URL,
		interval:       time.Minute,
		history:        urltest.NewHistoryStorage(),
		interruptGroup: interrupt.NewGroup(),
		lxURLTestGroup: lxURLTestGroup{cancel: cancel, balancer: &balancer{poolSize: len(pool), poolTolerance: poolTolerance}},
	}
	s := &URLTest{outbound: g.outbound, logger: log.NewNOPFactory().NewLogger("test"), group: g, lxURLTest: lxURLTest{balancer: g.balancer}}
	s.rrWire()
	g.balancer.setSlots(pool)
	for _, tag := range tags {
		g.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now().Add(-time.Minute), Delay: 100})
	}
	return &healFixture{s: s, g: g, nodes: nodes}
}

func (f *healFixture) fresh(tags ...string) {
	for _, tag := range tags {
		f.g.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: 100})
	}
}

// fail runs the dial-error branch for a connection that failed through tag.
func (f *healFixture) fail(ctx context.Context, tag string) (net.Conn, bool) {
	return f.failAt(ctx, tag, time.Now())
}

// failAt is fail for a connection that started at start.
func (f *healFixture) failAt(ctx context.Context, tag string, start time.Time) (net.Conn, bool) {
	destination := M.ParseSocksaddrHostPort("example.org", 443)
	return f.s.rrHealDial(ctx, N.NetworkTCP, destination, f.nodes[tag], E.New("dial failed"), f.s.testPick(ctx, destination, start))
}

// testPick is the pick of a connection to destination that started at start.
func (s *URLTest) testPick(ctx context.Context, destination M.Socksaddr, start time.Time) rrPick {
	key := s.balancer.rrKeyOf(ctx, destination)
	return rrPick{key: key, shift: s.balancer.memory.shifts.shiftOf(key.table), start: start}
}

// waitCheck waits for the check and the life probes in flight to end and clears
// their gaps: what the passing time does.
func (f *healFixture) waitCheck(t *testing.T) {
	t.Helper()
	heal := &f.s.balancer.heal
	idle := func() bool {
		if heal.roundBusy() {
			return false
		}
		for _, entry := range f.s.balancer.slotsSnapshot() {
			if entry.track != nil && entry.track.lifeBusy.Load() {
				return false
			}
		}
		return true
	}
	for round := 0; round < 3; round++ { // a failed life probe hands over to the check
		waitUntil(t, idle)
		time.Sleep(5 * time.Millisecond)
	}
	waitUntil(t, idle)
	heal.clearPause()
	heal.lifeAccess.Lock()
	heal.lifeStarts, heal.udpLifeLast = [rrLifeBurst]int64{}, 0
	heal.lifeAccess.Unlock()
	for _, entry := range f.s.balancer.slotsSnapshot() {
		if entry.track != nil {
			entry.track.lifeLast.Store(0)
		}
	}
}

func (f *healFixture) pool() []string { return f.g.balancer.poolTags() }

func (f *healFixture) probes(tag string) int32 { return f.nodes[tag].probes.Load() }

func samePool(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func closeConn(conn net.Conn) {
	if conn != nil {
		conn.Close()
	}
}

// --- dead node ----------------------------------------------------------------------

// The scheduled check runs while self-heal checks strike and replace nodes: the
// slots are never mixed up and no node sits in two slots. Meaningful under -race.
func TestRRHeal_tickDuringCheck(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f"}
	f := newHealFixture(t, tags, []string{"a", "b", "c"}, 0)
	f.g.interval = time.Nanosecond // no tick pause
	stop := make(chan struct{})
	var wg sync.WaitGroup
	observe := func() {
		pool := f.pool()
		if len(pool) != 3 {
			t.Errorf("pool size changed: %v", pool)
		}
		seen := map[string]bool{}
		for _, tag := range pool {
			if tag != "" && seen[tag] { // "": slots left empty by D1 (R1)
				t.Errorf("%s in two slots: %v", tag, pool)
			}
			seen[tag] = true
		}
	}
	wg.Add(3)
	go func() { // the scheduled health-check
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = f.g.urlTest(f.g.ctx, false)
			observe()
		}
	}()
	go func() { // nodes blink
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			node := f.nodes[tags[i%len(tags)]]
			node.dead.Store(!node.dead.Load())
			time.Sleep(time.Millisecond)
		}
	}()
	go func() { // self-heal checks of whatever sits in the slots
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.s.balancer.heal.epochEnd.Store(0)
			if f.s.balancer.heal.tryHoldRound() {
				f.s.rrHealCheck()
			}
			observe()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	observe()
}

// --- limits -------------------------------------------------------------------------

// assertFrozen checks that a failure gets its retry and nothing else: no check, no
// probe (N0).
func assertFrozen(t *testing.T, f *healFixture, reason string) {
	t.Helper()
	dials := f.nodes["b"].dials.Load()
	probes := f.probes("a")
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	time.Sleep(20 * time.Millisecond)
	if !ok || f.nodes["b"].dials.Load() != dials+1 {
		t.Fatalf("%s: the retry goes on while frozen", reason)
	}
	if f.s.balancer.heal.roundBusy() || f.probes("a") != probes {
		t.Fatalf("%s: no check while frozen", reason)
	}
}

// A network change freezes the self-heal from the signal itself, also when its full
// run does not start: the device is paused (early return) or the health-check is
// busy (CheckOutbounds returns at once).
func TestRRHeal_networkChangeFreezesWithoutRun(t *testing.T) {
	p := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	p.nodes["a"].siteDown.Store(true)
	p.g.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(p.g.ctx))
	p.g.pause.DevicePause()
	p.s.InterfaceUpdated(context.Background())
	assertFrozen(t, p, "network change on a paused device")
	if p.probes("b") != 0 || p.probes("c") != 0 {
		t.Fatal("precondition: no run probes on a paused device")
	}

	c := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	c.nodes["a"].siteDown.Store(true)
	c.g.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(c.g.ctx))
	c.g.checking.Store(true)
	c.s.InterfaceUpdated(context.Background())
	time.Sleep(50 * time.Millisecond)
	if c.probes("b") != 0 {
		t.Fatal("precondition: a busy health-check makes the run return at once")
	}
	assertFrozen(t, c, "network change with a busy health-check")
	c.g.checking.Store(false)
}

// --- other --------------------------------------------------------------------------

// A client cancel is not a failure: no retry, no check.
func TestRRHeal_clientCancel(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	conn, ok := f.s.rrHealDial(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443), f.nodes["a"], context.Canceled, rrPick{start: time.Now()})
	closeConn(conn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn2, ok2 := f.fail(ctx, "a")
	closeConn(conn2)
	time.Sleep(20 * time.Millisecond)
	if ok || ok2 || f.nodes["b"].dials.Load() != 0 {
		t.Fatal("a client cancel gets no retry")
	}
	if f.s.balancer.heal.pauseEnd() != nil || f.probes("a") != 0 {
		t.Fatal("a client cancel starts no check")
	}
}

// A slot holding a urltest (least_test) gets no retry from the outer group, but its
// leaf is probed; a selector over a plain node is retried.
func TestRRHeal_nestedGroups(t *testing.T) {
	f := newHealFixture(t, []string{"x", "b", "c", "d"}, nil, 0)
	x := f.nodes["x"]
	x.siteDown.Store(true)
	inner := &URLTest{
		Adapter: outbound.NewAdapter(C.TypeURLTest, "inner", []string{N.NetworkTCP}, []string{"x"}),
		group: &URLTestGroup{
			outbounds: []adapter.Outbound{x},
			history:   f.g.history,
			tolerance: 50,
		},
	}
	f.g.outbound.(*fakeManager).byTag["inner"] = inner
	f.g.balancer.setSlots([]string{"inner", "b", "c"})
	conn, ok := f.s.rrHealDial(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443), inner, E.New("dial failed"), rrPick{start: time.Now()})
	closeConn(conn)
	if ok || f.nodes["b"].dials.Load() != 0 {
		t.Fatal("a nested urltest retries on its own: no outer retry")
	}
	f.waitCheck(t)
	if x.probes.Load() != 1 {
		t.Fatalf("the leaf of the nested urltest must be probed, got %d", x.probes.Load())
	}

	sel := &Selector{Adapter: outbound.NewAdapter(C.TypeSelector, "sel", nil, []string{"x"})}
	sel.selected.Store(x)
	f.g.outbound.(*fakeManager).byTag["sel"] = sel
	f.g.balancer.setSlots([]string{"sel", "b", "c"})
	conn, ok = f.s.rrHealDial(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443), sel, E.New("dial failed"), rrPick{start: time.Now()})
	closeConn(conn)
	if !ok || f.nodes["b"].dials.Load() != 1 {
		t.Fatal("a selector over a plain node is retried")
	}
	f.waitCheck(t)

	outer := &Selector{Adapter: outbound.NewAdapter(C.TypeSelector, "outer", nil, []string{"inner"})}
	outer.selected.Store(inner)
	if !rrNestedURLTest(outer) || rrNestedURLTest(sel) || rrNestedURLTest(x) {
		t.Fatal("selectors unwrap to the first non-selector; only a urltest blocks the retry")
	}
}

// replaceSlot writes one slot, publishes a new slice and refuses a stale request.
func TestRRHeal_replaceSlot(t *testing.T) {
	var changes int
	b := &balancer{onChange: func() { changes++ }}
	b.setSlots([]string{"a", "b", "c"})
	before := b.slotsSnapshot()
	if !b.replaceSlot("b", "x") || !samePool(b.poolTags(), "a", "x", "c") {
		t.Fatalf("b must be replaced in place, got %v", b.poolTags())
	}
	if before[1].tag != "b" {
		t.Fatal("a published slice must never change")
	}
	if b.replaceSlot("b", "y") || b.replaceSlot("a", "c") {
		t.Fatal("a stale or duplicating replacement must be refused")
	}
	if changes != 2 {
		t.Fatalf("onChange once per pool change, got %d", changes)
	}
}

// lx:end SPEC 138
