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
	"github.com/sagernet/sing-box/common/lxhandshake"
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
	// release ends the stalled dials with a handshake timeout (lxhandshake.ErrTimeout)
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
	host := destination.AddrString()
	if n.is(n.stall, host) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-n.releaseChan():
			return nil, lxhandshake.ErrTimeout
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

// destinationRefused is the error of the proxy node a that answered that the
// destination failed (adapter.DestinationRefusedLx): the site is under suspicion.
func destinationRefused() error {
	return &adapter.DestinationRefusedLx{Tag: "a", Err: E.New("dial failed: destination refused")}
}

// settleDeferred waits for the checks and verdict probes in flight and runs the
// checks the pending failures deferred to the next pick of their node, until none
// is left: what the passing time and the next picks do.
func (f *shiftFixture) settleDeferred(t *testing.T) {
	t.Helper()
	for round := 0; round < 10; round++ {
		f.waitCheck(t)
		due := false
		for _, entry := range f.s.balancer.slotsSnapshot() {
			if entry.track != nil && entry.track.checkAfter.Load() != 0 {
				due = true
				entry.track.checkAfter.Store(1)
				f.s.rrOnPick(context.Background(), entry.tag, entry.track, rrClock(time.Now()))
			}
		}
		if !due {
			return
		}
	}
	f.waitCheck(t)
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

// pickAt is the pick of a connection to host that started at start.
func (f *shiftFixture) pickAt(ctx context.Context, host string, start time.Time) rrPick {
	return f.s.testPick(ctx, siteAddr(host), start)
}

// shiftOn is a failure on node of a connection picked with the shift expected.
func (f *shiftFixture) shiftOn(key rrKey, expected int, node string) {
	f.s.rrShiftOn(key, expected, node, false, false)
}

// --- the destination is unreachable through a node --------------------------------

// a answers its probe, the key's dial through a fails: the key shifts by one, its
// next connection goes to the next slot; a key of another site on a stays.
func TestRRShift_aliveNodeShiftsKey(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	other := f.keyOn(t, "open.example", 0)

	if opened, answered := f.dial(t, key, "blocked.example"); !opened || !answered {
		t.Fatal("the retry through b must open and answer")
	}
	f.waitCheck(t)
	if shift, marked := f.shift(key, "blocked.example"); shift != 1 || marked {
		t.Fatalf("a is alive: the key shifts by 1, got %d marked=%v", shift, marked)
	}
	aDials, bDials := f.dials("a"), f.dials("b")
	if _, answered := f.dial(t, key, "blocked.example"); !answered || f.dials("a") != aDials || f.dials("b") != bDials+1 {
		t.Fatalf("the shifted key goes straight to b: a %d→%d, b %d→%d", aDials, f.dials("a"), bDials, f.dials("b"))
	}
	if _, answered := f.dial(t, other, "open.example"); !answered || f.dials("a") != aDials+1 {
		t.Fatal("the key of another site on a stays on a")
	}
	if !samePool(f.pool(), "a", "b", "c") || f.g.history.LoadURLTestHistory("a") == nil {
		t.Fatalf("an alive node keeps its slot and history, pool %v", f.pool())
	}
}

// The dial through a and the retry through b fail; a answers the probe, b proves
// alive after its failure (a first byte through it): shift 2.
func TestRRShift_retryFailsToo(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	f.fb["b"].set(&f.fb["b"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	if opened, _ := f.dial(t, key, "blocked.example"); opened {
		t.Fatal("both attempts fail")
	}
	f.waitCheck(t)
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 && shift != 2 {
		t.Fatalf("a answered its probe: shift at least 1, got %d", shift)
	}
	if _, answered := f.dial(t, f.keyOn(t, "open.example", 1), "open.example"); !answered {
		t.Fatal("another site through b answers")
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 2 {
		t.Fatalf("a and b alive: shift 2, got %d", shift)
	}
	cDials := f.dials("c")
	if _, answered := f.dial(t, key, "blocked.example"); !answered || f.dials("c") != cDials+1 {
		t.Fatal("the key goes to c now")
	}
}

// Unreachable through every node: after the circle the key is an address
// problem — its own slot, no retries, no probes; the scheduled run lifts the mark.
func TestRRShift_fullCircleMarksAddress(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, node := range f.fb {
		node.set(&node.openFail, "gone.example", true)
	}
	key := f.keyOn(t, "gone.example", 0)
	f.dial(t, key, "gone.example") // a, retry b: shift 2
	f.settleDeferred(t)
	f.dial(t, key, "gone.example") // c, retry a: c passed, the circle is complete
	f.settleDeferred(t)
	if shift, marked := f.shift(key, "gone.example"); !marked || shift != 0 {
		t.Fatalf("the circle must mark the key, got shift %d marked %v", shift, marked)
	}
	dials := map[string]int32{"a": f.dials("a"), "b": f.dials("b"), "c": f.dials("c")}
	probes := f.probes("a") + f.probes("b") + f.probes("c")
	if opened, _ := f.dial(t, key, "gone.example"); opened {
		t.Fatal("the destination is gone")
	}
	time.Sleep(20 * time.Millisecond)
	if f.dials("a") != dials["a"]+1 || f.dials("b") != dials["b"] || f.dials("c") != dials["c"] {
		t.Fatalf("a marked key goes to its own slot, no retry: %v → a=%d b=%d c=%d", dials, f.dials("a"), f.dials("b"), f.dials("c"))
	}
	if f.probes("a")+f.probes("b")+f.probes("c") != probes || f.s.balancer.heal.running.Load() {
		t.Fatal("a marked key starts no check")
	}
	f.g.balancePool(context.Background())
	if _, marked := f.shift(key, "gone.example"); marked {
		t.Fatal("the scheduled run lifts the mark")
	}
}

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

// A lazy node opens without the network and closes without a byte: a failure, no
// retry; the first byte of another connection through the node settles it at once
// and the key shifts, with no probe.
func TestRRShift_lazyNodeClosesWithoutByte(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	f.s.balancer.trackOf("a").verdictLast.Store(rrClock(time.Now())) // this test covers the byte path, not the verdict probe
	key := f.keyOn(t, "blocked.example", 0)
	opened, answered := f.dial(t, key, "blocked.example")
	if !opened || answered {
		t.Fatal("precondition: the lazy node opens and answers nothing")
	}
	waitUntil(t, func() bool { return f.s.balancer.trackOf("a").pendingN.Load() == 1 })
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 {
		t.Fatalf("no proof yet: no shift, got %d", shift)
	}
	if _, answered := f.dial(t, f.keyOn(t, "open.example", 0), "open.example"); !answered {
		t.Fatal("another site through a answers")
	}
	if f.dials("b") != 0 {
		t.Fatal("a close without a byte gets no retry")
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("the key shifts, got %d", shift)
	}
	if f.probes("a") != 0 || f.s.balancer.heal.running.Load() {
		t.Fatal("a first byte settles it: no probe")
	}
	if _, answered := f.dial(t, key, "blocked.example"); !answered || f.dials("b") != 1 {
		t.Fatal("the next connection of the key goes to b")
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

// Without stickiness there is no table.
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
	if f.fails("a") != 1 {
		t.Fatalf("the retry's first byte confirms a's failure without stickiness, got %d", f.fails("a"))
	}
}

// The network is gone: a failure on a with history from before it and no byte
// since stays pending — no shift by history, no probe in the gap. History of a
// written after the failure settles it at the next pick of a that asks for it. And
// when nobody answers the check, nothing shifts and the self-heal freezes.
func TestRRShift_networkGoneNoShiftByHistory(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fresh("a", "b", "c")
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	end := time.Now()
	f.s.balancer.heal.lastCheckEnd.Store(&end)                       // the gap: no check now
	f.s.balancer.trackOf("a").verdictLast.Store(rrClock(time.Now())) // and no verdict probe
	conn, _ := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], destinationRefused(), f.pickAt(key, "blocked.example", time.Now()))
	closeConn(conn)
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 {
		t.Fatalf("fresh history from before the failure: no shift, got %d", shift)
	}
	if f.probes("a") != 0 {
		t.Fatal("the gap: no probe")
	}
	track := f.s.balancer.trackOf("a")
	if track.pendingN.Load() != 1 || track.checkAfter.Load() == 0 {
		t.Fatal("the failure is pending and asks for a check later")
	}
	// History written by another run right after the failure: its probe may have
	// started before the failure (it took up to the probe timeout), so it proves
	// nothing about the time after it.
	time.Sleep(time.Millisecond)
	f.fresh("a")
	track.checkAfter.Store(1) // the wait is over
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("blocked.example"))
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 {
		t.Fatalf("history less than the probe timeout after the failure settles nothing, got %d", shift)
	}
	// History written the probe timeout or more after the failure: its probe
	// started after the failure.
	f.g.history.StoreURLTestHistory("a", &adapter.URLTestHistory{Time: time.Now().Add(C.TCPTimeout + time.Second), Delay: 100})
	f.s.balancer.heal.lastCheckEnd.Store(&end) // still the gap
	track.checkAfter.Store(1)
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("blocked.example"))
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("history written the probe timeout after the failure settles it, got %d", shift)
	}
	if f.probes("a") != 0 {
		t.Fatal("settled by history: no probe")
	}

	// All silent: the check probes, nobody answers — the own network, no shift.
	g := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	g.fresh("a", "b", "c")
	for _, node := range g.fb {
		node.dead.Store(true)
	}
	key = g.keyOn(t, "blocked.example", 0)
	g.dial(t, key, "blocked.example")
	g.waitCheck(t)
	if g.s.balancer.memory.shifts.size.Load() != 0 || !g.s.balancer.heal.frozen() {
		t.Fatal("nobody answered: no shift, frozen")
	}
}

// --- a node that drops connections ------------------------------------------------

// Three confirmed failures of a (its retries got the first byte), none of the
// neighbours, all of them with enough connections: a is quarantined, its slot refilled, its history kept; the
// scheduled run lifts the quarantine.
func TestRRShift_quarantine(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < rrQuarantineMinConns; i++ {
			f.s.balancer.rrCountConn(tag, &f.s.balancer.heal)
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

// Equal confirmed failures everywhere: nobody is quarantined.
func TestRRShift_equalFailuresNoQuarantine(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 10; i++ {
			f.s.balancer.rrCountConn(tag, &f.s.balancer.heal)
		}
	}
	for i := 0; i < 5; i++ {
		for _, tag := range []string{"a", "b", "c"} {
			f.s.rrCreditFail(tag)
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
			g.s.balancer.rrCountConn(tag, &g.s.balancer.heal)
		}
	}
	g.s.rrCreditFail("b")
	g.s.rrCreditFail("c")
	g.s.rrCreditFail("a")
	g.s.rrCreditFail("a")
	if g.s.balancer.isQuarantined("a") {
		t.Fatal("two failures are below the floor")
	}
	g.s.rrCreditFail("a")
	if !g.s.balancer.isQuarantined("a") {
		t.Fatal("3 failures, share 0.3 above twice the median 0.1: quarantine")
	}
}

// The retry's first byte confirms a's failure once; the key's later first byte
// through b does not confirm it again.
func TestRRShift_confirmOnce(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	f.dial(t, key, "blocked.example")
	f.waitCheck(t)
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("precondition: shift 1, got %d", shift)
	}
	f.dial(t, key, "blocked.example")
	f.dial(t, key, "blocked.example")
	if f.fails("a") != 1 {
		t.Fatalf("a's failure is confirmed once, got %d", f.fails("a"))
	}
}

// A key that passed a node and then got the first byte elsewhere confirms the
// failure of that node once (case 2); a marked key confirms nothing.
func TestRRShift_keyConfirmsPassedNodes(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	f.s.balancer.trackOf("a").verdictLast.Store(rrClock(time.Now())) // this test covers the byte path, not the verdict probe
	key := f.keyOn(t, "blocked.example", 0)
	f.dial(t, key, "blocked.example") // silent close on a: pending, no retry
	waitUntil(t, func() bool { return f.s.balancer.trackOf("a").pendingN.Load() == 1 })
	f.dial(t, f.keyOn(t, "open.example", 0), "open.example") // a byte through a: the key shifts
	if f.fails("a") != 0 {
		t.Fatal("precondition: no confirmation yet")
	}
	f.dial(t, key, "blocked.example") // through b: answers
	f.dial(t, key, "blocked.example")
	if f.fails("a") != 1 {
		t.Fatalf("the key confirms a's failure once, got %d", f.fails("a"))
	}

	var table rrShiftTable
	table.access.Lock()
	entry := table.entry(7)
	entry.passed = []rrPassedNode{{tag: "a"}}
	entry.marked = true
	table.access.Unlock()
	if tags := table.creditable(7, "b", ""); len(tags) != 0 {
		t.Fatalf("a marked key confirms nothing, got %v", tags)
	}
}

// At 256 connections both counters halve.
func TestRRShift_countersHalve(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	b := f.s.balancer
	for i := 0; i < 10; i++ {
		b.rrCountConn("a", &b.heal)
	}
	b.memory.access.Lock()
	b.memory.counts["a"].fails = 9
	b.memory.access.Unlock()
	for i := 10; i < rrCountHalveAt; i++ {
		b.rrCountConn("a", &b.heal)
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

// Frozen: no shifts and no counting.
func TestRRShift_frozenRecordsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	key := f.s.balancer.rrKeyOf(f.keyOn(t, "blocked.example", 0), siteAddr("blocked.example"))
	f.s.balancer.heal.freeze()
	f.s.rrShiftOn(key, 0, "a", false, false)
	f.s.balancer.rrCountConn("a", &f.s.balancer.heal)
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "b", key: key, confirmFor: "a"})
	if f.s.balancer.memory.shifts.size.Load() != 0 || f.fails("a") != 0 {
		t.Fatal("frozen: no shifts, no confirmed failures")
	}
	f.s.balancer.memory.access.Lock()
	counted := len(f.s.balancer.memory.counts)
	f.s.balancer.memory.access.Unlock()
	if counted != 0 {
		t.Fatal("frozen: no connection counts")
	}
}

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
			f.s.balancer.heal.lastCheckEnd.Store(nil)
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	waitUntil(t, func() bool { return !f.s.balancer.heal.running.Load() && !f.g.checking.Load() })
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
	balancer.alive = func(string) bool { return true }
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
