// lx:begin SPEC 138 round_robin self-heal tests (stage 1)

package group

import (
	"context"
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
//   - slow: probes take this long before they answer or fail.
type healNode struct {
	adapter.Outbound
	tag      string
	server   string
	refused  string
	dead     atomic.Bool
	siteDown atomic.Bool
	hold     chan struct{}
	slow     time.Duration
	probes   atomic.Int32
	dials    atomic.Int32
	direct   bool
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
	} else {
		n.dials.Add(1)
	}
	if n.refused != "" {
		return net.Dial("tcp", n.refused)
	}
	if n.dead.Load() || (!probe && n.siteDown.Load()) {
		return nil, E.New("node ", n.tag, " down")
	}
	return net.Dial("tcp", n.server)
}

type healFixture struct {
	s     *URLTest
	g     *URLTestGroup
	nodes map[string]*healNode
}

// newHealFixture builds a round_robin group over the tags with the given pool. Every
// node starts alive with stale history (probed when checked); pool_tolerance as given.
func newHealFixture(t testing.TB, tags []string, pool []string, poolTolerance uint16) *healFixture {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	byTag := make(map[string]adapter.Outbound, len(tags))
	nodes := make(map[string]*healNode, len(tags))
	outbounds := make([]adapter.Outbound, 0, len(tags))
	for _, tag := range tags {
		node := &healNode{tag: tag, server: address}
		nodes[tag] = node
		byTag[tag] = node
		outbounds = append(outbounds, node)
	}
	g := &URLTestGroup{
		ctx:            ctx,
		cancel:         cancel,
		outbound:       &fakeManager{byTag: byTag},
		logger:         log.NewNOPFactory().NewLogger("test"),
		outbounds:      outbounds,
		link:           server.URL,
		interval:       time.Minute,
		history:        urltest.NewHistoryStorage(),
		balancer:       &balancer{poolSize: len(pool), poolTolerance: poolTolerance},
		interruptGroup: interrupt.NewGroup(),
	}
	s := &URLTest{outbound: g.outbound, logger: log.NewNOPFactory().NewLogger("test"), group: g, balancer: g.balancer}
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

// waitCheck waits for the background check to end and clears the probe gap. The
// check slot is taken before the call that starts the check returns, so a check
// that ended without a probe (and set no gap) is waited for too.
func (f *healFixture) waitCheck(t *testing.T) {
	t.Helper()
	waitUntil(t, func() bool {
		return !f.s.balancer.heal.running.Load() && f.s.balancer.heal.verdicts.Load() == 0
	})
	waitUntil(t, func() bool { // a silent verdict probe hands over to the check
		return !f.s.balancer.heal.running.Load() && f.s.balancer.heal.verdicts.Load() == 0
	})
	f.s.balancer.heal.lastCheckEnd.Store(nil)
	f.s.balancer.heal.lastHangEnd.Store(nil)
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

// Pool [a,b,c], a dead: the connection opens through b, a's history is dropped and
// its slot taken by the first answering node outside the pool; b and c keep their
// slots. b is the control and is probed live despite its fresh history; c is not
// probed.
func TestRRHeal_deadNodeRetriedStruckReplaced(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	f.fresh("b", "c")
	f.nodes["a"].dead.Store(true)
	f.nodes["d"].dead.Store(true)
	f.s.balancer.stickyHash = nil // counter rotation: the first pick is slot 0

	conn, err := f.s.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal("the connection must open through the next slot: ", err)
	}
	closeConn(conn)
	if f.nodes["b"].dials.Load() != 1 {
		t.Fatalf("retry must go through b, b dials=%d", f.nodes["b"].dials.Load())
	}
	if got := f.g.lastSelected.Load(); got != "b" {
		t.Fatalf("the last pick must be the retry node, got %q", got)
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("dead a must lose its history")
	}
	if pool := f.pool(); !samePool(pool, "e", "b", "c") {
		t.Fatalf("a's slot must go to e (d is silent), got %v", pool)
	}
	if f.probes("b") != 1 || f.probes("c") != 0 {
		t.Fatalf("control b is probed live despite fresh history, c is not probed: b=%d c=%d", f.probes("b"), f.probes("c"))
	}
	if h := f.g.history.LoadURLTestHistory("d"); h == nil || time.Since(h.Time) < 30*time.Second {
		t.Fatal("the silence of a candidate is not written: d keeps its old history")
	}
	if h := f.g.history.LoadURLTestHistory("e"); h == nil || time.Since(h.Time) > time.Second {
		t.Fatal("the answer of the replacement must be written")
	}
}

// A refused connection (RST from a stopped node server) is a failure like a timeout.
func TestRRHeal_refusedIsLikeTimeout(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.nodes["a"].refused = listener.Addr().String()
	listener.Close()
	f.fresh("b", "c")

	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if !ok {
		t.Fatal("a refused connection must be retried")
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil || !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("refused a must be struck and replaced, pool %v", f.pool())
	}
}

// A and B silent, the third answers: both are struck. The third silent too: the
// local network — nothing changes, the group freezes.
func TestRRHeal_bothSilentThirdDecides(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].dead.Store(true)
	f.nodes["b"].dead.Store(true)
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if ok {
		t.Fatal("the retry through dead b must fail")
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil || f.g.history.LoadURLTestHistory("b") != nil {
		t.Fatal("third c answered: a and b must both be struck")
	}
	if pool := f.pool(); !samePool(pool, "d", "e", "c") {
		t.Fatalf("a and b must be replaced in their slots, got %v", pool)
	}

	g := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	for _, tag := range []string{"a", "b", "c", "d"} {
		g.nodes[tag].dead.Store(true)
	}
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	g.waitCheck(t)
	for _, tag := range []string{"a", "b", "c"} {
		if g.g.history.LoadURLTestHistory(tag) == nil {
			t.Fatalf("own network: history of %s must stay", tag)
		}
	}
	if !samePool(g.pool(), "a", "b", "c") || !g.s.balancer.heal.frozen() {
		t.Fatalf("own network: slots unchanged %v and frozen %v", g.pool(), g.s.balancer.heal.frozen())
	}
}

// A pool of one: no retry; a candidate outside the pool is the control and the
// replacement; silent candidates change nothing.
func TestRRHeal_singleSlotPool(t *testing.T) {
	f := newHealFixture(t, []string{"a", "d"}, []string{"a"}, 0)
	f.nodes["a"].dead.Store(true)
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if ok {
		t.Fatal("a pool of one has no retry")
	}
	f.waitCheck(t)
	if !samePool(f.pool(), "d") || f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatalf("answering d must take a's slot, got %v", f.pool())
	}

	g := newHealFixture(t, []string{"a", "d"}, []string{"a"}, 0)
	g.nodes["a"].dead.Store(true)
	g.nodes["d"].dead.Store(true)
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	g.waitCheck(t)
	if !samePool(g.pool(), "a") || g.g.history.LoadURLTestHistory("a") == nil {
		t.Fatalf("silent candidates: nothing changes, got %v", g.pool())
	}
}

// A struck node that comes back does not return to its slot.
func TestRRHeal_revivedNodeStaysOut(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	f.fresh("b", "c")
	f.nodes["a"].dead.Store(true)
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	f.waitCheck(t)
	if !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("precondition: a replaced by d, got %v", f.pool())
	}
	f.nodes["a"].dead.Store(false)
	f.fresh("a")
	f.g.balancePoolFirstLive(context.Background(), 3)
	if !samePool(f.pool(), "d", "b", "c") {
		t.Fatalf("revived a must stay out of the pool, got %v", f.pool())
	}
}

// pool_tolerance > 0: the replacement is the fastest out-of-pool node by history,
// unprobed; without history it is found by probes.
func TestRRHeal_tolerantReplacement(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 50)
	f.fresh("b", "c")
	f.g.history.StoreURLTestHistory("d", &adapter.URLTestHistory{Time: time.Now().Add(-time.Minute), Delay: 80})
	f.g.history.StoreURLTestHistory("e", &adapter.URLTestHistory{Time: time.Now().Add(-time.Minute), Delay: 20})
	f.nodes["a"].dead.Store(true)
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	f.waitCheck(t)
	if !samePool(f.pool(), "e", "b", "c") {
		t.Fatalf("fastest e by history must replace a, got %v", f.pool())
	}
	if f.probes("d") != 0 || f.probes("e") != 0 {
		t.Fatalf("history replacement must not probe: d=%d e=%d", f.probes("d"), f.probes("e"))
	}

	g := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 50)
	g.fresh("b", "c")
	g.g.history.DeleteURLTestHistory("d")
	g.g.history.DeleteURLTestHistory("e")
	g.nodes["a"].dead.Store(true)
	g.nodes["d"].dead.Store(true)
	conn, _ = g.fail(context.Background(), "a")
	closeConn(conn)
	g.waitCheck(t)
	if !samePool(g.pool(), "e", "b", "c") || g.probes("d") != 1 || g.probes("e") != 1 {
		t.Fatalf("no history: replacement by probes, got %v d=%d e=%d", g.pool(), g.probes("d"), g.probes("e"))
	}
}

// A answers its probe: alive, the destination is the problem. Its fresh answer is
// written, the pool does not change.
func TestRRHeal_aliveNodeKept(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].siteDown.Store(true)
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	if !ok {
		t.Fatal("the retry through b must open")
	}
	f.waitCheck(t)
	if h := f.g.history.LoadURLTestHistory("a"); h == nil || time.Since(h.Time) > time.Second {
		t.Fatal("a answered: its fresh history must be written")
	}
	if h := f.g.history.LoadURLTestHistory("b"); h == nil || time.Since(h.Time) > time.Second {
		t.Fatal("b answered too: its fresh history must be written")
	}
	if !samePool(f.pool(), "a", "b", "c") || f.probes("d") != 0 {
		t.Fatalf("alive a: pool unchanged and no candidate probed, got %v", f.pool())
	}
}

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
			if seen[tag] {
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
			pool := f.pool()
			member := f.nodes[pool[i%len(pool)]]
			neighbour := f.nodes[pool[(i+1)%len(pool)]]
			f.s.balancer.heal.frozenUntil.Store(nil)
			f.s.rrHealCheck(member, neighbour, false)
			observe()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	observe()
}

// --- limits -------------------------------------------------------------------------

// A hundred concurrent failures through a: one check; within the gap the retries go
// on and no new check starts.
func TestRRHeal_oneCheckInFlightAndGap(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.nodes["a"].siteDown.Store(true)
	f.nodes["a"].hold = make(chan struct{})
	var wg sync.WaitGroup
	var retried atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, ok := f.fail(context.Background(), "a")
			closeConn(conn)
			if ok {
				retried.Add(1)
			}
		}()
	}
	wg.Wait()
	close(f.nodes["a"].hold)
	waitUntil(t, func() bool { return !f.s.balancer.heal.running.Load() && f.s.balancer.heal.lastCheckEnd.Load() != nil })
	if retried.Load() != 100 {
		t.Fatalf("every failure must be retried, got %d", retried.Load())
	}
	if f.probes("a") != 1 {
		t.Fatalf("one check in flight: a probed %d times", f.probes("a"))
	}
	end := f.s.balancer.heal.lastCheckEnd.Load()
	f.g.history.StoreURLTestHistory("a", &adapter.URLTestHistory{Time: time.Now().Add(-time.Minute), Delay: 100})
	for i := 0; i < 10; i++ {
		conn, ok := f.fail(context.Background(), "a")
		closeConn(conn)
		if !ok {
			t.Fatal("retries go on in the gap")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if f.probes("a") != 1 || f.s.balancer.heal.lastCheckEnd.Load() != end {
		t.Fatalf("no new check in the gap: a probed %d times", f.probes("a"))
	}
}

// Fresh history is no verdict any more: a failed node with history younger than
// 30 s and a first byte of the group since is probed all the same; only a proof
// after the failure — its byte, its probe, its history — settles the failure.
func TestRRHeal_freshHistoryIsNoVerdict(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.fresh("a", "b")
	f.nodes["a"].siteDown.Store(true)
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now()))
	conn, _ := f.failAt(context.Background(), "a", time.Now().Add(-time.Second))
	closeConn(conn)
	f.waitCheck(t)
	if f.probes("a") != 1 {
		t.Fatalf("fresh a is probed, got %d", f.probes("a"))
	}
	if !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a answered: pool unchanged, got %v", f.pool())
	}
}

// assertFrozen checks that a failure gets neither a retry nor a check.
func assertFrozen(t *testing.T, f *healFixture, reason string) {
	t.Helper()
	dials := f.nodes["b"].dials.Load()
	probes := f.probes("a")
	conn, ok := f.fail(context.Background(), "a")
	closeConn(conn)
	time.Sleep(20 * time.Millisecond)
	if ok || f.nodes["b"].dials.Load() != dials {
		t.Fatalf("%s: no retry while frozen", reason)
	}
	if f.s.balancer.heal.running.Load() || f.probes("a") != probes {
		t.Fatalf("%s: no check while frozen", reason)
	}
}

// Freeze: the "own network" verdict, a wake signal and the full run after a network
// change — no retries, no probes.
func TestRRHeal_freeze(t *testing.T) {
	// own network
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	for _, node := range f.nodes {
		node.dead.Store(true)
	}
	conn, _ := f.fail(context.Background(), "a")
	closeConn(conn)
	f.waitCheck(t)
	for _, node := range f.nodes {
		node.dead.Store(false)
	}
	assertFrozen(t, f, "own network")

	// wake
	w := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	w.nodes["a"].siteDown.Store(true)
	w.g.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(w.g.ctx))
	w.g.rrWatchWake()
	w.g.pause.DevicePause()
	w.g.pause.DeviceWake()
	assertFrozen(t, w, "device wake")
	w.s.balancer.heal.frozenUntil.Store(nil)
	w.g.pause.NetworkPause()
	w.g.pause.NetworkWake()
	assertFrozen(t, w, "network wake")

	// the full run after a network change
	n := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	n.nodes["a"].siteDown.Store(true)
	n.nodes["c"].hold = make(chan struct{})
	n.g.pause = service.FromContext[pause.Manager](pause.WithDefaultManager(n.g.ctx))
	n.s.InterfaceUpdated(context.Background())
	waitUntil(t, func() bool { return n.probes("a") == 1 && n.probes("b") == 1 && n.probes("c") == 1 })
	assertFrozen(t, n, "network change run")
	close(n.nodes["c"].hold)
	waitUntil(t, func() bool { return n.s.balancer.heal.networkRuns.Load() == 0 })
	if !n.s.balancer.heal.frozen() {
		t.Fatal("the freeze of the network change signal outlives a short run")
	}
	n.s.balancer.heal.frozenUntil.Store(nil)
	conn, ok := n.fail(context.Background(), "a")
	closeConn(conn)
	if !ok {
		t.Fatal("after the network change freeze the retry is back")
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
	if p.s.balancer.heal.networkRuns.Load() != 0 {
		t.Fatal("precondition: a paused device starts no run")
	}
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
	if c.s.balancer.heal.networkRuns.Load() != 0 || c.probes("b") != 0 {
		t.Fatal("precondition: a busy health-check makes the run return at once")
	}
	assertFrozen(t, c, "network change with a busy health-check")
	c.g.checking.Store(false)
}

// pool_tolerance 0: the candidates outside the pool are probed in parallel batches
// of the pool size. The first one in config order (d) is silent, the second (e)
// answers: the replacement takes the time of one probe, not two; every answer of
// the batch is written, the silence is not. As a control the search is the same.
func TestRRHeal_replacementProbesInParallel(t *testing.T) {
	const slow = 300 * time.Millisecond
	for _, asControl := range []bool{false, true} {
		f := newHealFixture(t, []string{"a", "b", "c", "d", "e", "x"}, []string{"a", "b", "c"}, 0)
		f.fresh("b", "c")
		f.g.history.DeleteURLTestHistory("a")
		f.nodes["d"].dead.Store(true)
		for _, tag := range []string{"d", "e", "x"} {
			f.nodes[tag].slow = slow
		}
		started := time.Now()
		answer, probed, found := f.s.rrReplace(context.Background(), rrProbe{member: "a", leaf: "a", started: time.Now()}, asControl)
		elapsed := time.Since(started)
		if !found || answer.member != "e" || !samePool(f.pool(), "e", "b", "c") {
			t.Fatalf("control=%v: e must replace a, got %v (answer %q)", asControl, f.pool(), answer.member)
		}
		if elapsed >= 2*slow {
			t.Fatalf("control=%v: the batch must be probed in parallel, took %v", asControl, elapsed)
		}
		if probed != 3 || f.probes("d") != 1 || f.probes("e") != 1 || f.probes("x") != 1 {
			t.Fatalf("control=%v: one batch of the pool size, probed=%d d=%d e=%d x=%d", asControl, probed, f.probes("d"), f.probes("e"), f.probes("x"))
		}
		if h := f.g.history.LoadURLTestHistory("d"); h == nil || time.Since(h.Time) < 30*time.Second {
			t.Fatalf("control=%v: the silence of d is not written", asControl)
		}
		for _, tag := range []string{"e", "x"} {
			if h := f.g.history.LoadURLTestHistory(tag); h == nil || time.Since(h.Time) > time.Second {
				t.Fatalf("control=%v: the answer of %s in the batch must be written", asControl, tag)
			}
		}
	}

	// A first batch with no answer: the next batch is probed.
	g := newHealFixture(t, []string{"a", "b", "d", "e", "x"}, []string{"a", "b"}, 0)
	g.fresh("b")
	g.g.history.DeleteURLTestHistory("a")
	g.nodes["d"].dead.Store(true)
	g.nodes["e"].dead.Store(true)
	answer, probed, found := g.s.rrReplace(context.Background(), rrProbe{member: "a", leaf: "a", started: time.Now()}, false)
	if !found || answer.member != "x" || probed != 3 || !samePool(g.pool(), "x", "b") {
		t.Fatalf("second batch: x must replace a, got %v probed=%d", g.pool(), probed)
	}
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
	if f.s.balancer.heal.lastCheckEnd.Load() != nil || f.probes("a") != 0 {
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

// The neighbour is the next live slot going round; dead and unusable slots are
// skipped, the failed slot itself never is the neighbour.
func TestRRHeal_nextLiveSlot(t *testing.T) {
	b := &balancer{}
	b.setSlots([]string{"a", "b", "c", "d"})
	slots := b.slotsSnapshot()
	live := liveTags("a", "b", "d")
	for from, want := range map[int]int{0: 1, 1: 3, 2: 3, 3: 0} {
		if got, ok := b.nextLiveSlot(slots, from, live.alive); !ok || got != want {
			t.Fatalf("from %d: got %d %v, want %d", from, got, ok, want)
		}
	}
	if _, ok := b.nextLiveSlot(slots, 0, liveTags("a").alive); ok {
		t.Fatal("no other live slot: no neighbour")
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
