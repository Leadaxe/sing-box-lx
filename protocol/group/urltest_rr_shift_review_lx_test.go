// lx:begin SPEC 138 round_robin stage 2 review tests

package group

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// setCounts sets the slot counters of tag.
func (f *shiftFixture) setCounts(tag string, conns, fails uint32) {
	m := &f.s.balancer.memory
	m.access.Lock()
	defer m.access.Unlock()
	c := m.count(tag)
	c.conns, c.fails = conns, fails
}

func (f *shiftFixture) hasCounts(tag string) bool {
	m := &f.s.balancer.memory
	m.access.Lock()
	defer m.access.Unlock()
	_, found := m.counts[tag]
	return found
}

// A read that ran into the consumer's deadline is no outcome: no failure, and the
// connection stays watched — its later first byte is the success.
func TestRRReview_readTimeoutIsNoOutcome(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn, f := watched(t, client)
	f.nodes["a"].siteDown.Store(true)
	release := make(chan struct{})
	go func() {
		buffer := make([]byte, 8)
		server.Read(buffer)
		<-release
		server.Write([]byte("ok"))
	}()
	conn.Write([]byte("hi"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	buffer := make([]byte, 8)
	if n, err := conn.Read(buffer); n != 0 || !E.IsTimeout(err) {
		t.Fatalf("precondition: a read timeout, got %d %v", n, err)
	}
	time.Sleep(20 * time.Millisecond)
	if f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastCheckEnd.Load() != nil {
		t.Fatal("a read timeout is no failure: no check")
	}
	conn.SetReadDeadline(time.Time{})
	close(release)
	if n, _ := conn.Read(buffer); n == 0 {
		t.Fatal("precondition: the answer")
	}
	if !provedAny(f.s.balancer) {
		t.Fatal("the connection is still watched after a timeout: its first byte counts")
	}
}

// cancelNode cancels the client's context on a dial to a destination: the client
// gave up while the retry was opening.
type cancelNode struct {
	*fbNode
	cancel context.CancelFunc
}

func (n *cancelNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if destination.String() == n.server {
		return n.fbNode.DialContext(ctx, network, destination)
	}
	n.dials.Add(1)
	n.cancel()
	return nil, ctx.Err()
}

// The retry through b ends in a client cancel: no failure of b — no count, no
// shift past b.
func TestRRReview_retryCancelledIsNoFailure(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	ctx, cancel := context.WithCancel(key)
	defer cancel()
	b := &cancelNode{fbNode: f.fb["b"], cancel: cancel}
	f.g.outbound.(*fakeManager).byTag["b"] = b
	f.g.outbounds[1] = b
	if _, err := f.s.DialContext(ctx, N.NetworkTCP, siteAddr("blocked.example")); err == nil {
		t.Fatal("precondition: the connection fails")
	}
	if f.dials("b") != 1 {
		t.Fatal("precondition: the retry went to b")
	}
	f.waitCheck(t)
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("a alive: shift 1, the cancelled retry does not shift past b, got %d", shift)
	}
	if f.hasCounts("b") {
		t.Fatal("a cancelled retry is no connection of b")
	}
}

// A connection that outlived a freeze (a network change, then a full run that wrote
// fresh history to every node) gives no verdict at all: no shift, no check — it
// started before an event of the network. One that started after the freeze gets
// the probe, and the probe decides.
func TestRRReview_freezeSinceStartNeedsProbe(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	start := time.Now()
	time.Sleep(time.Millisecond)
	heal := &f.s.balancer.heal
	heal.freeze()
	past := time.Now().Add(-time.Second)
	heal.frozenUntil.Store(&past) // the freeze is over
	f.fresh("a", "b", "c")        // the full run after the network change
	end := time.Now()
	heal.lastCheckEnd.Store(&end) // the gap: no probe now
	conn, _ := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], destinationRefused(), f.pickAt(key, "blocked.example", start))
	closeConn(conn)
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 {
		t.Fatalf("a freeze since the start: no shift by history, got %d", shift)
	}

	heal.lastCheckEnd.Store(nil)
	probes := f.probes("a")
	conn, _ = f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], destinationRefused(), f.pickAt(key, "blocked.example", start))
	closeConn(conn)
	f.waitCheck(t)
	if f.probes("a") != probes {
		t.Fatal("a connection from before the freeze starts no check")
	}
	conn, _ = f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], destinationRefused(), f.pickAt(key, "blocked.example", time.Now()))
	closeConn(conn)
	f.waitCheck(t)
	if f.probes("a") == probes {
		t.Fatal("a connection after the freeze: a is probed live")
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("a answered the probe: shift 1, got %d", shift)
	}
}

// A key fails again on a node it passed already (the pool was rebuilt under it):
// it shifts on; a stale connection of the same key changes nothing.
func TestRRReview_passedNodeShiftsAgain(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "blocked.example", 0)
	key := f.s.balancer.rrKeyOf(ctx, siteAddr("blocked.example"))
	f.shiftOn(key, 0, "a")
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 1 {
		t.Fatalf("precondition: shift 1, got %d", shift)
	}
	// Parallel connections picked with shift 0: the key is shifted already.
	f.shiftOn(key, 0, "a")
	f.shiftOn(key, 0, "b")
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 1 {
		t.Fatalf("a connection picked before the shift changes nothing, got %d", shift)
	}
	// The pool is rebuilt: the key's slot 1 holds a, which it passed.
	f.s.balancer.setSlots([]string{"b", "a", "c"})
	f.shiftOn(key, 1, "a")
	if shift, marked := f.shift(ctx, "blocked.example"); shift != 2 || marked {
		t.Fatalf("a failure on a passed node shifts the key on: want 2, got %d marked %v", shift, marked)
	}
}

// Nodes that left the pool do not count toward the circle.
func TestRRReview_departedNodesLeaveCircle(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "blocked.example", 0)
	key := f.s.balancer.rrKeyOf(ctx, siteAddr("blocked.example"))
	f.shiftOn(key, 0, "a")
	f.shiftOn(key, 1, "b")
	f.s.balancer.setSlots([]string{"d", "e", "c"})
	f.shiftOn(key, 2, "c")
	if shift, marked := f.shift(ctx, "blocked.example"); marked || shift != 3 {
		t.Fatalf("d and e were never tried: no mark, shift 3; got %d marked %v", shift, marked)
	}
}

// The median takes only nodes with at least 8 connections; the candidate needs 8 too.
func TestRRReview_medianOnlyBusyNodes(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c", "d"})
	f.setCounts("a", 10, 2)
	f.setCounts("b", 10, 2)
	// c and d are fresh: no connections yet.
	f.s.rrCreditFail("a")
	if f.s.balancer.isQuarantined("a") {
		t.Fatal("share 0.3 is not above twice the median 0.2 of the busy nodes")
	}

	g := newShiftFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c", "d"})
	g.setCounts("a", 5, 2)
	for _, tag := range []string{"b", "c", "d"} {
		g.setCounts(tag, 10, 0)
	}
	g.s.rrCreditFail("a")
	if g.s.balancer.isQuarantined("a") {
		t.Fatal("a node with fewer than 8 connections is no quarantine candidate")
	}

	h := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	h.setCounts("a", 10, 2)
	h.s.rrCreditFail("a")
	if h.s.balancer.isQuarantined("a") {
		t.Fatal("no busy neighbour: no quarantine")
	}
}

// The failures a node collected while it was dead do not follow it after the dead
// verdict.
func TestRRReview_deadVerdictResetsCounters(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		f.setCounts(tag, 10, 0)
	}
	f.setCounts("a", 10, 1)
	f.fb["a"].dead.Store(true)
	ctx := f.keyOn(t, "site.example", 0)
	if _, answered := f.dial(t, ctx, "site.example"); !answered {
		t.Fatal("precondition: the retry answers")
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("precondition: a struck out")
	}
	// a comes back with no replacement taken.
	f.fb["a"].dead.Store(false)
	f.fresh("a")
	f.s.rrCreditFail("a")
	if f.s.balancer.isQuarantined("a") || f.fails("a") > 1 {
		t.Fatalf("the dead verdict resets a's counters: fails %d", f.fails("a"))
	}
}

// The reset of the quarantine and a quarantine in progress are serialized: the
// lifted quarantine does not come back.
func TestRRReview_resetMarksUnderLock(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	m := &f.s.balancer.memory
	m.quarantine.Store(&map[string]bool{"a": true})
	// A quarantine in progress (rrCountFail) holds access and copies the set.
	m.access.Lock()
	set := m.quarantine.Load()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.s.balancer.rrResetMarks()
	}()
	time.Sleep(20 * time.Millisecond)
	next := map[string]bool{"b": true}
	if set != nil {
		for tag := range *set {
			next[tag] = true
		}
	}
	m.quarantine.Store(&next)
	m.access.Unlock()
	wg.Wait()
	if f.s.balancer.isQuarantined("a") {
		t.Fatal("the lifted quarantine came back")
	}
}

// One connection credits a node once: the retry's first byte and the key's passed
// list both name a.
func TestRRReview_noDoubleCreditInOneConnection(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	key := f.s.balancer.rrKeyOf(f.keyOn(t, "blocked.example", 0), siteAddr("blocked.example"))
	table := &f.s.balancer.memory.shifts
	table.access.Lock()
	entry := table.entry(key.table)
	entry.shift = 1
	entry.passed = []rrPassedNode{{tag: "a"}}
	table.access.Unlock()
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "b", key: key, confirmFor: "a"})
	if f.fails("a") != 1 {
		t.Fatalf("one connection credits a once, got %d", f.fails("a"))
	}
}

// The pool membership is checked under the counters' lock: a node that left the
// pool meanwhile gets no orphan counter.
func TestRRReview_countsRecheckPoolUnderLock(t *testing.T) {
	for _, count := range []struct {
		name string
		run  func(f *shiftFixture)
	}{
		{"connection", func(f *shiftFixture) { f.s.balancer.rrCountConn("a", &f.s.balancer.heal) }},
		{"failure", func(f *shiftFixture) { f.s.balancer.rrCountFail("a", rrKey{}) }},
	} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		b := f.s.balancer
		b.memory.access.Lock()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			count.run(f)
		}()
		time.Sleep(20 * time.Millisecond)
		b.access.Lock()
		b.slots = []slot{{tag: "d"}, {tag: "b"}, {tag: "c"}}
		b.access.Unlock()
		b.memory.access.Unlock()
		wg.Wait()
		if f.hasCounts("a") {
			t.Fatalf("%s: a left the pool, no counter", count.name)
		}
	}
}

// The refill of a quarantined slot is a check: it waits out the gap and ends
// through endCheck.
func TestRRReview_quarantineReplaceRespectsGap(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		f.setCounts(tag, 10, 0)
	}
	heal := &f.s.balancer.heal
	end := time.Now()
	heal.lastCheckEnd.Store(&end)
	for i := 0; i < 3; i++ {
		f.s.rrCreditFail("a")
	}
	if !f.s.balancer.isQuarantined("a") {
		t.Fatal("precondition: a quarantined")
	}
	time.Sleep(150 * time.Millisecond)
	if f.probes("d") != 0 || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("the gap: no probes, no refill yet; pool %v", f.pool())
	}
	gapOver := time.Now().Add(-rrHealGap)
	heal.lastCheckEnd.Store(&gapOver)
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	waitUntil(t, func() bool {
		last := heal.lastCheckEnd.Load()
		return !heal.running.Load() && last != nil && last.After(gapOver)
	})
}

// The sticky hash built without strings is the hash of the sticky key: the slots
// of the keys do not move. The table key of an IP destination is unchanged too.
func TestRRReview_stickyHashWithoutStrings(t *testing.T) {
	b := &balancer{stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickySourceIP, C.URLTestStickyDomain, C.URLTestStickyDestIP, C.URLTestStickyDestPort}}
	metadata := &adapter.InboundContext{
		ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/usr/bin/app"},
		Source:      M.ParseSocksaddrHostPort("2001:db8::1", 5000),
		Domain:      "example.com",
	}
	ctx := adapter.WithContext(context.Background(), metadata)
	for _, destination := range []M.Socksaddr{
		M.ParseSocksaddrHostPort("203.0.113.7", 443),
		M.ParseSocksaddrHostPort("2001:db8::2", 8443),
		M.ParseSocksaddrHostPort("example.com", 80),
		{},
	} {
		if got, want := b.rrKeyOf(ctx, destination).sticky, hashKey(b.stickyKey(ctx, destination)); got != want {
			t.Fatalf("%s: sticky hash %x, want %x", destination, got, want)
		}
		if got, want := b.rrKeyOf(context.Background(), destination).sticky, hashKey(b.stickyKey(context.Background(), destination)); got != want {
			t.Fatalf("%s without metadata: sticky hash %x, want %x", destination, got, want)
		}
	}
	process := &balancer{stickyHash: []string{C.URLTestStickyProcess}}
	destination := M.ParseSocksaddrHostPort("203.0.113.7", 443)
	key := process.rrKeyOf(ctx, destination)
	want := fnvString(fnvString(hashKey(process.stickyKey(ctx, destination)), "\x00\x00"), "example.com")
	if key.table != want || key.label(process.stickyHash) != "example.com" {
		t.Fatalf("domain table key %x, want %x", key.table, want)
	}
	key = process.rrKeyOf(context.Background(), destination)
	want = fnvString(fnvString(hashKey(process.stickyKey(context.Background(), destination)), "\x00\x00"), "203.0.113.7")
	if key.table != want || key.label(process.stickyHash) != "203.0.113.7" {
		t.Fatalf("IP table key %x, want %x", key.table, want)
	}
	// The log label of an IP destination carries the port when the sticky key holds it.
	ported := &balancer{stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickyDestPort}}
	if label := ported.rrKeyOf(context.Background(), destination).label(ported.stickyHash); label != "203.0.113.7:443" {
		t.Fatalf("an IP label with the port in the key: got %q", label)
	}
	v6 := M.ParseSocksaddrHostPort("2001:db8::2", 8443)
	if label := ported.rrKeyOf(context.Background(), v6).label(ported.stickyHash); label != "[2001:db8::2]:8443" {
		t.Fatalf("an IPv6 label with the port in the key: got %q", label)
	}
}

var _ adapter.Outbound = (*cancelNode)(nil)

// lx:end SPEC 138
