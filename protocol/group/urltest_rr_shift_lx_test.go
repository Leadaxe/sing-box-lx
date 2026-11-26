// lx:begin SPEC 138 round_robin key shift, slot counters and quarantine tests (stage 2)

package group

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// fbNode is a proxy-like fake node (not direct, so its connections are watched).
// A probe goes to the group's test server through healNode; a connection is a
// net.Pipe whose far end plays the destination:
//   - siteDown (healNode) or openFail[host]: the dial fails, the probe answers;
//   - silent[host]: the connection opens, reads what the client sent and closes
//     without a byte (a lazy node: VLESS/mux hand out the connection first);
//   - hangup[host]: the connection opens and closes at once;
//   - mute[host]: the connection opens, reads and never answers;
//   - stall[host]: the dial hangs until its context is done (a node that accepts
//     and stays silent: a stopped server behind a SOCKS handshake);
//   - otherwise the destination answers "ok".
type fbNode struct {
	*healNode
	access   sync.Mutex
	openFail map[string]bool
	silent   map[string]bool
	hangup   map[string]bool
	mute     map[string]bool
	stall    map[string]bool
	// release ends the stalled dials with a handshake timeout (context.DeadlineExceeded, as the upstream clients return)
	release chan struct{}
}

// Type is VLESS (a proxy leaf); with direct set (healNode) a direct outbound.
func (n *fbNode) Type() string {
	if n.direct {
		return C.TypeDirect
	}
	return C.TypeVLESS
}

func (n *fbNode) releaseChan() chan struct{} {
	n.access.Lock()
	defer n.access.Unlock()
	return n.release
}

func (n *fbNode) set(m *map[string]bool, host string, on bool) {
	n.access.Lock()
	defer n.access.Unlock()
	if *m == nil {
		*m = make(map[string]bool)
	}
	(*m)[host] = on
}

func (n *fbNode) is(m map[string]bool, host string) bool {
	n.access.Lock()
	defer n.access.Unlock()
	return m[host]
}

func (n *fbNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if destination.String() == n.server {
		return n.healNode.DialContext(ctx, network, destination)
	}
	n.dials.Add(1)
	if n.dialErr != nil {
		return nil, n.dialErr
	}
	host := destination.AddrString()
	if n.is(n.stall, host) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-n.releaseChan():
			return nil, context.DeadlineExceeded
		}
	}
	if n.is(n.openFail, host) {
		// The proxy answers that the destination failed (a SOCKS failure code, an
		// HTTP CONNECT 502): the site's fault.
		return nil, &adapter.DestinationRefusedLx{Tag: n.tag, Err: E.New("node ", n.tag, " cannot reach ", host)}
	}
	if n.dead.Load() || n.siteDown.Load() {
		return nil, E.New("node ", n.tag, " down")
	}
	client, server := net.Pipe()
	silent, hangup, mute := n.is(n.silent, host), n.is(n.hangup, host), n.is(n.mute, host)
	go func() {
		defer server.Close()
		if hangup {
			return
		}
		buffer := make([]byte, 64)
		if _, err := server.Read(buffer); err != nil || silent {
			return
		}
		if mute {
			_, _ = io.Copy(io.Discard, server)
			return
		}
		if _, err := server.Write([]byte("ok")); err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

type shiftFixture struct {
	*healFixture
	fb map[string]*fbNode
}

// newShiftFixture is newHealFixture with fbNode nodes and stickiness on
// [process, domain].
func newShiftFixture(t *testing.T, tags []string, pool []string) *shiftFixture {
	t.Helper()
	f := newHealFixture(t, tags, pool, 0)
	manager := f.g.outbound.(*fakeManager)
	fb := make(map[string]*fbNode, len(tags))
	for i, tag := range tags {
		node := &fbNode{healNode: f.nodes[tag]}
		fb[tag] = node
		manager.byTag[tag] = node
		f.g.outbounds[i] = node
	}
	f.s.balancer.stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
	return &shiftFixture{healFixture: f, fb: fb}
}

func siteAddr(host string) M.Socksaddr {
	return M.ParseSocksaddrHostPort(host, 443)
}

// keyOn returns the context of a connection to host whose sticky slot is slot.
func (f *shiftFixture) keyOn(t *testing.T, host string, slot int) context.Context {
	t.Helper()
	n := uint64(len(f.pool()))
	destination := siteAddr(host)
	for i := 0; i < 10000; i++ {
		metadata := &adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/app/" + strconv.Itoa(i)}}
		if destination.IsFqdn() {
			metadata.Domain = host
		}
		ctx := adapter.WithContext(context.Background(), metadata)
		if hashKey(f.s.balancer.stickyKey(ctx, destination))%n == uint64(slot) {
			return ctx
		}
	}
	t.Fatal("no key for slot ", slot)
	return nil
}

// dial opens a connection of the group to host and exchanges one message: the
// client sends, the destination answers or not. ok is whether a byte came back.
func (f *shiftFixture) dial(t *testing.T, ctx context.Context, host string) (opened bool, answered bool) {
	t.Helper()
	conn, err := f.s.DialContext(ctx, N.NetworkTCP, siteAddr(host))
	if err != nil {
		return false, false
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("hi")); err != nil {
		return true, false
	}
	buffer := make([]byte, 8)
	n, _ := conn.Read(buffer)
	return true, n > 0
}

func (f *shiftFixture) shift(ctx context.Context, host string) (int, bool) {
	key := f.s.balancer.rrKeyOf(ctx, siteAddr(host))
	table := &f.s.balancer.memory.shifts
	table.access.Lock()
	defer table.access.Unlock()
	if e := table.entries[key.table]; e != nil {
		return e.shift, e.marked
	}
	return 0, false
}

func (f *shiftFixture) fails(tag string) uint32 {
	m := &f.s.balancer.memory
	m.access.Lock()
	defer m.access.Unlock()
	if c := m.counts[tag]; c != nil {
		return c.fails
	}
	return 0
}

func (f *shiftFixture) dials(tag string) int32 { return f.fb[tag].dials.Load() }

// shiftOn is a failure on node of a connection picked with the shift expected.
func (f *shiftFixture) shiftOn(key rrKey, expected int, node string) {
	f.s.rrShiftOn(key, expected, node, rrShiftK1)
}

// --- the destination is unreachable through a node --------------------------------

// A connection by IP with no domain: the shift is kept under the destination IP;
// another address of the same application is not shifted.
func TestRRShift_ipWithoutDomain(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "192.0.2.1", true)
	key := f.keyOn(t, "192.0.2.1", 0)
	f.dial(t, key, "192.0.2.1")
	f.waitCheck(t)
	if shift, _ := f.shift(key, "192.0.2.1"); shift != 1 {
		t.Fatalf("the IP key shifts, got %d", shift)
	}
	aDials := f.dials("a")
	if _, answered := f.dial(t, key, "192.0.2.2"); !answered || f.dials("a") != aDials+1 {
		t.Fatal("another address of the same application stays on a")
	}
	if shift, _ := f.shift(key, "192.0.2.2"); shift != 0 {
		t.Fatal("another address is not shifted")
	}
}

// The table holds at most 1024 keys and drops the oldest.
func TestRRShift_tableBounded(t *testing.T) {
	var table rrShiftTable
	table.access.Lock()
	for key := uint64(1); key <= 3000; key++ {
		table.entry(key).shift = 1
	}
	table.access.Unlock()
	if len(table.entries) != rrShiftTableSize || table.size.Load() != rrShiftTableSize {
		t.Fatalf("the table must hold %d keys, got %d", rrShiftTableSize, len(table.entries))
	}
	if table.shiftOf(1) != 0 || table.shiftOf(3000) != 1 || table.shiftOf(3000-rrShiftTableSize+1) != 1 || table.shiftOf(3000-rrShiftTableSize) != 0 {
		t.Fatal("the oldest keys go first")
	}
}

// Without stickiness there is no table, no K1 and so no K1 count (V3).
func TestRRShift_noStickinessNoShift(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.s.balancer.stickyHash = nil
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	if _, answered := f.dial(t, context.Background(), "blocked.example"); !answered {
		t.Fatal("the retry answers")
	}
	f.waitCheck(t)
	if f.s.balancer.memory.shifts.size.Load() != 0 {
		t.Fatal("no stickiness: no shift table")
	}
	if f.fails("a") != 0 {
		t.Fatalf("the explicit refusal counts through K1 only, which needs a key, got %d", f.fails("a"))
	}
}

// --- a node that drops connections ------------------------------------------------

// Scenario 16, Q2: three counted failures of a (each retry answered, a answered its life probe), none of the
// neighbours, all of them with enough connections: a is quarantined, its slot refilled, its history kept; the
// scheduled run lifts the quarantine.
func TestRRShift_quarantine(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < rrQuarantineMinConns; i++ {
			f.s.balancer.rrCountConn(tag, time.Now())
		}
	}
	f.fb["a"].siteDown.Store(true)
	for i := 0; i < 3; i++ {
		host := "site" + strconv.Itoa(i) + ".example"
		if _, answered := f.dial(t, f.keyOn(t, host, 0), host); !answered {
			t.Fatal("the retry answers")
		}
		f.waitCheck(t) // a proxy whose dial failed counts once a probe finds it alive
	}
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	if !f.s.balancer.isQuarantined("a") || f.s.slotAlive("a") {
		t.Fatal("a is in quarantine and skipped")
	}
	if f.g.history.LoadURLTestHistory("a") == nil {
		t.Fatal("a quarantined node keeps its history")
	}
	f.waitCheck(t)
	f.g.balancePool(context.Background())
	if f.s.balancer.isQuarantined("a") {
		t.Fatal("the scheduled run lifts the quarantine")
	}
}

// Equal counted failures everywhere: nobody is quarantined.
func TestRRShift_equalFailuresNoQuarantine(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 10; i++ {
			f.s.balancer.rrCountConn(tag, time.Now())
		}
	}
	for i := 0; i < 5; i++ {
		for _, tag := range []string{"a", "b", "c"} {
			f.s.rrCreditFail(tag, rrKey{})
		}
	}
	for _, tag := range []string{"a", "b", "c"} {
		if f.s.balancer.isQuarantined(tag) {
			t.Fatalf("%s quarantined with failures equal to its neighbours", tag)
		}
	}
	// The threshold: 3 failures and more than twice the median.
	g := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 10; i++ {
			g.s.balancer.rrCountConn(tag, time.Now())
		}
	}
	g.s.rrCreditFail("b", rrKey{})
	g.s.rrCreditFail("c", rrKey{})
	g.s.rrCreditFail("a", rrKey{})
	g.s.rrCreditFail("a", rrKey{})
	if g.s.balancer.isQuarantined("a") {
		t.Fatal("two failures are below the floor")
	}
	g.s.rrCreditFail("a", rrKey{})
	if !g.s.balancer.isQuarantined("a") {
		t.Fatal("3 failures, share 0.3 above twice the median 0.1: quarantine")
	}
}

// At 256 connections both counters halve.
func TestRRShift_countersHalve(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	b := f.s.balancer
	for i := 0; i < 10; i++ {
		b.rrCountConn("a", time.Now())
	}
	b.memory.access.Lock()
	b.memory.counts["a"].fails = 9
	b.memory.access.Unlock()
	for i := 10; i < rrCountHalveAt; i++ {
		b.rrCountConn("a", time.Now())
	}
	b.memory.access.Lock()
	count := *b.memory.counts["a"]
	b.memory.access.Unlock()
	if count.conns != rrCountHalveAt/2 || count.fails != 4 {
		t.Fatalf("at %d connections both halve: got conns %d fails %d", rrCountHalveAt, count.conns, count.fails)
	}
	// A node that takes a slot starts from zero.
	b.setSlots([]string{"x", "b", "c"})
	b.setSlots([]string{"a", "b", "c"})
	b.memory.access.Lock()
	_, kept := b.memory.counts["a"]
	b.memory.access.Unlock()
	if kept {
		t.Fatal("a node that left the pool loses its counters")
	}
}

// --- limits -------------------------------------------------------------------------

// Many connections, failures, first bytes, ticks and slot changes at once: no data
// race, no node in two slots. Meaningful under -race.
func TestRRShift_concurrent(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	f.fb["b"].set(&f.fb["b"].silent, "blocked.example", true)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var dials atomic.Int32
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				host := "blocked.example"
				if i%2 == 1 {
					host = "open" + strconv.Itoa(i%7) + ".example"
				}
				metadata := &adapter.InboundContext{Domain: host, ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/app/" + strconv.Itoa(i%13)}}
				f.dial(t, adapter.WithContext(context.Background(), metadata), host)
				dials.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f.s.balancer.rrResetMarks()
			f.s.balancer.heal.clearPause()
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	waitUntil(t, func() bool { return !f.s.balancer.heal.roundBusy() && !f.g.checking.Load() })
	pool := f.pool()
	seen := map[string]bool{}
	for _, tag := range pool {
		if seen[tag] {
			t.Fatalf("%s in two slots: %v", tag, pool)
		}
		seen[tag] = true
	}
	if dials.Load() == 0 {
		t.Fatal("no connections ran")
	}
}

// lx:end SPEC 138

// pick on the hot path with a non-empty shift table: an IP destination without a
// domain, so the table key carries the destination IP.
func BenchmarkRRPick(b *testing.B) {
	balancer := &balancer{poolSize: 3, stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickyDomain}}
	balancer.setSlots([]string{"a", "b", "c"})
	balancer.rr = allAlive{}
	nodes := map[string]adapter.Outbound{}
	for _, tag := range []string{"a", "b", "c"} {
		nodes[tag] = &healNode{tag: tag}
	}
	resolve := func(tag string) adapter.Outbound { return nodes[tag] }
	table := &balancer.memory.shifts
	table.access.Lock()
	table.entry(12345).shift = 1
	table.access.Unlock()
	metadata := &adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/usr/bin/app"}}
	ctx := adapter.WithContext(context.Background(), metadata)
	destination := M.ParseSocksaddrHostPort("203.0.113.7", 443)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if balancer.pick(ctx, destination, nil, resolve) == nil {
			b.Fatal("no pick")
		}
	}
}
