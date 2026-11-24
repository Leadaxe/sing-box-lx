// lx:begin SPEC 138 round_robin unfinished connections and pending failures tests

package group

import (
	"context"
	"math"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// ageTrack moves every unfinished connection of track d into the past.
func ageTrack(track *rrTrack, d time.Duration) {
	track.access.Lock()
	defer track.access.Unlock()
	for _, list := range []*rrTrackList{&track.dials, &track.waits} {
		for e := list.head; e != nil; e = e.next {
			e.since -= int64(d)
		}
		if list.head != nil {
			list.oldest.Store(list.head.since)
		}
	}
}

type dialResult struct {
	conn net.Conn
	err  error
}

// dialAsync opens a connection of the group in the background.
func (f *shiftFixture) dialAsync(ctx context.Context, host string) chan dialResult {
	done := make(chan dialResult, 1)
	go func() {
		conn, err := f.s.DialContext(ctx, N.NetworkTCP, siteAddr(host))
		done <- dialResult{conn, err}
	}()
	return done
}

func waitDial(t *testing.T, done chan dialResult) dialResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("the dial is still hanging")
		return dialResult{}
	}
}

// --- II: a node that accepts and stays silent -------------------------------------

// a accepts and stays silent: its dial hangs. A pick of its slot sooner than 5 s
// is no reason; later it is — the check finds a dead, strikes it out and cancels
// its dial in flight, which retries once through b with no new check. No shift,
// no count.
func TestRRTrack_hungDialCancelledByDeadVerdict(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].stall, "site.example", true)
	key := f.keyOn(t, "site.example", 0)
	track := f.s.balancer.trackOf("a")
	done := f.dialAsync(key, "site.example")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })

	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example"))
	time.Sleep(20 * time.Millisecond)
	if f.s.balancer.heal.running.Load() || f.probes("a") != 0 {
		t.Fatal("a dial in flight for less than 5 s is no reason")
	}

	ageTrack(track, rrHangAfter+time.Second)
	f.fb["a"].dead.Store(true) // its probe fails too
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example"))
	result := waitDial(t, done)
	if result.err != nil {
		t.Fatal("the cancelled dial retries through the next live slot: ", result.err)
	}
	defer result.conn.Close()
	if f.dials("b") != 1 {
		t.Fatalf("the retry goes through b, got %d dials", f.dials("b"))
	}
	result.conn.Write([]byte("hi"))
	buffer := make([]byte, 8)
	if n, _ := result.conn.Read(buffer); n == 0 {
		t.Fatal("the retry answers")
	}
	f.waitCheck(t)
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("a is struck out")
	}
	if f.probes("a") != 1 {
		t.Fatalf("one check, no new one for the cancelled dial: a probed %d times", f.probes("a"))
	}
	if shift, _ := f.shift(key, "site.example"); shift != 0 || f.fails("a") != 0 || f.s.balancer.memory.shifts.size.Load() != 0 {
		t.Fatalf("the dead verdict shifts and counts nothing: shift %d fails %d", shift, f.fails("a"))
	}
	if track.dials.oldest.Load() != 0 {
		t.Fatal("no dial left in flight")
	}
}

// a hands out connections and never answers (VLESS to a stopped server): the
// connection whose client sent and got no byte for 5 s is a reason at the next pick
// of the slot; the dead verdict closes it. A connection through a that got its
// first byte is left alone.
func TestRRTrack_waitingConnectionClosedByDeadVerdict(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "mute.example", true)
	key := f.keyOn(t, "mute.example", 0)
	waiting, err := f.s.DialContext(key, N.NetworkTCP, siteAddr("mute.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	answered, err := f.s.DialContext(f.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer answered.Close()
	answered.Write([]byte("hi"))
	buffer := make([]byte, 8)
	if n, _ := answered.Read(buffer); n == 0 {
		t.Fatal("precondition: a byte through a")
	}
	track := f.s.balancer.trackOf("a")
	if track.waits.oldest.Load() != 0 {
		t.Fatal("before the client sends, the connection waits for nothing")
	}
	waiting.Write([]byte("hi"))
	if track.waits.oldest.Load() == 0 {
		t.Fatal("the client sent: the connection waits for its first byte")
	}
	read := make(chan error, 1)
	go func() {
		_, err := waiting.Read(make([]byte, 8))
		read <- err
	}()

	ageTrack(track, rrHangAfter+time.Second)
	track.proved.Store(rrClock(time.Now().Add(-time.Minute))) // that byte came before the wait began
	f.fb["a"].dead.Store(true)
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("mute.example"))
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("the waiting connection is closed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the dead verdict must close the connection waiting for a first byte")
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("a is struck out")
	}
	inner, _ := common.Cast[*rrFirstByteConn](answered)
	if inner == nil || inner.state.Load()&rrStateClosed != 0 {
		t.Fatal("a connection that got its first byte is left alone")
	}
	if f.s.balancer.trackOf("a") != nil || track.waits.oldest.Load() != 0 {
		t.Fatal("the struck node holds no slot and no waiting connection")
	}
}

// Nobody answers the check (the own network): nothing is cancelled or closed.
func TestRRTrack_ownNetworkCancelsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].stall, "site.example", true)
	f.fb["a"].set(&f.fb["a"].mute, "mute.example", true)
	key := f.keyOn(t, "site.example", 0)
	waiting, err := f.s.DialContext(f.keyOn(t, "mute.example", 0), N.NetworkTCP, siteAddr("mute.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	waiting.Write([]byte("hi"))
	ctx, cancel := context.WithCancel(key)
	defer cancel()
	done := f.dialAsync(ctx, "site.example")
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	ageTrack(track, rrHangAfter+time.Second)
	for _, node := range f.fb {
		node.dead.Store(true)
	}
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example"))
	waitUntil(t, func() bool { return f.probes("a") == 1 && !f.s.balancer.heal.running.Load() })
	if !f.s.balancer.heal.frozen() {
		t.Fatal("precondition: the own network verdict")
	}
	select {
	case <-done:
		t.Fatal("the own network verdict cancels no dial")
	case <-time.After(50 * time.Millisecond):
	}
	inner, _ := common.Cast[*rrFirstByteConn](waiting)
	if inner == nil || inner.state.Load()&rrStateClosed != 0 || track.waits.oldest.Load() == 0 {
		t.Fatal("the own network verdict closes no connection")
	}
	cancel()
	if result := waitDial(t, done); result.err == nil {
		result.conn.Close()
	}
}

// A half-close from our side before the first byte does not void the later
// reason: the client half-closed (VLESS closes that way), then closed after the wait.
func TestRRTrack_halfCloseKeepsReason(t *testing.T) {
	for _, viaRoute := range []bool{false, true} {
		f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
		client, server := tcpPair(t)
		go func() {
			buffer := make([]byte, 8)
			server.Read(buffer)
		}()
		conn := newRRFirstByteConn(client, &rrConnWatch{s: f.s, member: "a", track: f.s.balancer.trackOf("a"), start: time.Now().Add(-rrHangAfter - time.Second)})
		conn.Write([]byte("hi"))
		if viaRoute {
			conn.LxClientFinished(true)
		} else if err := conn.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastHangEnd.Load() != nil {
			t.Fatal("a half-close alone is no reason")
		}
		conn.Close()
		waitUntil(t, func() bool { return f.s.balancer.heal.lastHangEnd.Load() != nil })
		if f.probes("a") != 1 {
			t.Fatalf("viaRoute=%v: the close after the half-close is a reason: a probed %d times", viaRoute, f.probes("a"))
		}
	}
}

// The tracker goes with its node: a node keeps it across slot changes, a node that
// leaves takes its pending failures with it, a newcomer starts empty.
func TestRRTrack_followsNode(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	b := f.s.balancer
	a, bTrack := b.trackOf("a"), b.trackOf("b")
	a.addPending(rrPending{failure: f.s.rrNewFailure(rrPick{}, "a"), at: rrClock(time.Now())})
	b.setSlots([]string{"d", "b", "c"})
	if b.trackOf("a") != nil || a.pendingN.Load() != 0 || len(a.pending) != 0 {
		t.Fatal("a node out of the pool takes its pending failures with it")
	}
	if b.trackOf("b") != bTrack || b.trackOf("d") == nil {
		t.Fatal("a node that stays keeps its tracker; a newcomer gets one")
	}
	b.setSlots([]string{"a", "b", "c"})
	if fresh := b.trackOf("a"); fresh == nil || fresh == a {
		t.Fatal("a node back in the pool starts a fresh tracker")
	}
	c := b.trackOf("c")
	c.addPending(rrPending{failure: f.s.rrNewFailure(rrPick{}, "c"), at: rrClock(time.Now())})
	if !b.replaceSlot("c", "d") || c.pendingN.Load() != 0 || b.trackOf("d") == nil {
		t.Fatal("a replaced node takes its pending failures with it")
	}
}

// A UDP flow waits for its first packet from the client's first one; the dead
// verdict closes it; the first packet back takes it off the list.
func TestRRTrack_udpFlowWaits(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	track := f.s.balancer.trackOf("a")
	open := func() (*rrFirstPacketConn, net.PacketConn) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		return newRRFirstPacketConn(pc, &rrConnWatch{s: f.s, member: "a", track: track, packet: true, start: time.Now()}), pc
	}
	echo, pc := open()
	if _, err := echo.WriteTo([]byte("x"), pc.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if track.waits.oldest.Load() == 0 {
		t.Fatal("a flow that sent waits for an answer")
	}
	if _, _, err := echo.ReadFrom(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if track.waits.oldest.Load() != 0 {
		t.Fatal("the first packet back ends the wait")
	}
	silent, _ := open()
	silent.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if track.kill() != 1 {
		t.Fatal("the dead verdict ends the waiting flow")
	}
	if silent.state.Load()&rrStateClosed == 0 {
		t.Fatal("the waiting flow is closed")
	}
	silent.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := silent.ReadFrom(make([]byte, 8)); err == nil {
		t.Fatal("a closed flow reads nothing")
	}
}

// The pick allocates nothing: with unfinished connections younger than 5 s, and
// with an old one looked at lately.
func TestRRTrack_pickAllocatesNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "site.example", 0)
	destination := siteAddr("site.example")
	resolve := func(tag string) adapter.Outbound { return f.fb[tag] }
	track := f.s.balancer.trackOf("a")
	var entry rrTrackEntry
	track.track(&track.waits, &entry, rrClock(time.Now()), nopKill{})
	if allocs := testing.AllocsPerRun(1000, func() { f.s.balancer.pickKeyed(ctx, destination, nil, resolve) }); allocs != 0 {
		t.Fatalf("pick with a young unfinished connection allocates %v", allocs)
	}
	ageTrack(track, rrHangAfter+time.Second)
	track.hangLook.Store(rrClock(time.Now().Add(time.Hour)))
	if allocs := testing.AllocsPerRun(1000, func() { f.s.balancer.pickKeyed(ctx, destination, nil, resolve) }); allocs != 0 {
		t.Fatalf("pick with an old unfinished connection looked at lately allocates %v", allocs)
	}
}

type nopKill struct{}

func (nopKill) rrKill() {}

// --- III: a failure waits for the verdict on its node -----------------------------

// A proof from before the failure settles nothing; one after it does.
func TestRRPending_proofAfterFailureOnly(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "blocked.example", 0)
	picked := f.pickAt(ctx, "blocked.example", time.Now())
	track := f.s.balancer.trackOf("a")
	at := rrClock(time.Now())
	track.addPending(rrPending{failure: f.s.rrNewFailure(picked, "a"), at: at})
	f.s.rrSettleAlive(track, at-1)
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 0 || track.pendingN.Load() != 1 {
		t.Fatal("a proof from before the failure settles nothing")
	}
	f.s.rrSettleAlive(track, at+1)
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 1 || track.pendingN.Load() != 0 {
		t.Fatal("a proof after the failure settles it")
	}
}

// A freeze voids the failures pending before it: a byte after it settles nothing.
func TestRRPending_freezeVoids(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	f.s.balancer.trackOf("a").verdictLast.Store(rrClock(time.Now())) // this test covers the byte path, not the verdict probe
	key := f.keyOn(t, "blocked.example", 0)
	f.dial(t, key, "blocked.example")
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return track.pendingN.Load() == 1 })
	time.Sleep(time.Millisecond)
	f.s.balancer.heal.lastFreeze.Store(rrClock(time.Now())) // a freeze that has passed
	f.dial(t, f.keyOn(t, "open.example", 0), "open.example")
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 {
		t.Fatalf("a failure before the freeze is void, got shift %d", shift)
	}
	if track.pendingN.Load() != 0 {
		t.Fatal("a void failure is dropped")
	}
}

// The dead verdict drops the node's pending failures: no shift, no count.
func TestRRPending_deadDropsPending(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	f.dial(t, key, "blocked.example")
	track := f.s.balancer.trackOf("a")
	waitUntil(t, func() bool { return track.pendingN.Load() == 1 })
	f.fb["a"].dead.Store(true)
	track.checkAfter.Store(1) // the wait is over
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("blocked.example"))
	f.waitCheck(t)
	waitUntil(t, func() bool { return samePool(f.pool(), "d", "b", "c") })
	if track.pendingN.Load() != 0 {
		t.Fatal("the dead verdict drops the pending failures")
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 0 || f.fails("a") != 0 {
		t.Fatal("a dead node shifts and counts nothing")
	}
}

// A close without a byte whose verdict probe cannot start (the node had one
// lately) probes nothing at once; the next pick of the node after rrVerdictWait
// with no verdict yet starts the check, and its answer settles it.
func TestRRPending_deferredCheckOnNextPick(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].silent, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	track := f.s.balancer.trackOf("a")
	track.verdictLast.Store(rrClock(time.Now()))
	f.dial(t, key, "blocked.example")
	waitUntil(t, func() bool { return track.pendingN.Load() == 1 })
	due := track.checkAfter.Load()
	if due == 0 || due-rrClock(time.Now()) > int64(rrVerdictWait) {
		t.Fatalf("the failure asks for a check rrVerdictWait later, got %d", due)
	}
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("blocked.example"))
	time.Sleep(20 * time.Millisecond)
	if f.probes("a") != 0 {
		t.Fatal("a pick sooner than rrVerdictWait starts nothing")
	}
	track.checkAfter.Store(1)
	f.s.selectBalanced(key, N.NetworkTCP, siteAddr("blocked.example"))
	f.waitCheck(t)
	if f.probes("a") != 1 {
		t.Fatalf("the pick after the wait starts the check, a probed %d", f.probes("a"))
	}
	if shift, _ := f.shift(key, "blocked.example"); shift != 1 {
		t.Fatalf("a answered: the key shifts, got %d", shift)
	}
}

// At most rrPendingMax failures per node, the oldest go; none older than rrPendingTTL.
func TestRRPending_bounded(t *testing.T) {
	track := newRRTrack("a")
	now := rrClock(time.Now())
	for i := 0; i < rrPendingMax+6; i++ {
		track.addPending(rrPending{at: now + int64(i)})
	}
	if len(track.pending) != rrPendingMax || track.pendingN.Load() != rrPendingMax || track.pending[0].at != now+6 {
		t.Fatalf("at most %d, the oldest go: got %d from %d", rrPendingMax, len(track.pending), track.pending[0].at-now)
	}
	old := newRRTrack("b")
	old.addPending(rrPending{at: now - int64(rrPendingTTL) - 1})
	old.addPending(rrPending{at: now})
	if settled := old.takeAlive(now+1, math.MinInt64, now); len(settled) != 1 || settled[0].at != now {
		t.Fatalf("a failure older than %v is dropped, got %d", rrPendingTTL, len(settled))
	}
}

// Three failures of a confirmed by the retries, in the gap (no probe): the first
// byte through a counts them all at once, and a goes to quarantine — no probe is
// needed.
func TestRRPending_quarantineWithoutProbes(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < rrQuarantineMinConns; i++ {
			f.s.balancer.rrCountConn(tag, &f.s.balancer.heal)
		}
	}
	end := time.Now()
	f.s.balancer.heal.lastCheckEnd.Store(&end)
	for i := 0; i < 3; i++ {
		host := "site" + string(rune('0'+i)) + ".example"
		f.fb["a"].set(&f.fb["a"].openFail, host, true)
		if _, answered := f.dial(t, f.keyOn(t, host, 0), host); !answered {
			t.Fatal("the retry answers")
		}
	}
	if f.fails("a") != 0 {
		t.Fatal("no proof yet: nothing counted")
	}
	f.dial(t, f.keyOn(t, "open.example", 0), "open.example")
	if f.fails("a") != 3 || !f.s.balancer.isQuarantined("a") {
		t.Fatalf("the first byte through a counts its three failures: got %d, quarantined %v", f.fails("a"), f.s.balancer.isQuarantined("a"))
	}
	if f.probes("a") != 0 {
		t.Fatal("no probe")
	}
}

// The pick hot path with the trackers and the pick hook wired: an IP destination,
// a non-empty shift table, an unfinished connection younger than 5 s on the slot.
func BenchmarkRRPickTracked(b *testing.B) {
	balancer := &balancer{poolSize: 3, stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickyDomain}}
	balancer.setSlots([]string{"a", "b", "c"})
	balancer.alive = func(string) bool { return true }
	balancer.onPick = func(context.Context, string, *rrTrack, int64) {}
	nodes := map[string]adapter.Outbound{}
	for _, tag := range []string{"a", "b", "c"} {
		nodes[tag] = &healNode{tag: tag}
		track := balancer.trackOf(tag)
		var entry rrTrackEntry
		track.track(&track.waits, &entry, rrClock(time.Now()), nopKill{})
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

// --- review follow-ups -------------------------------------------------------------

// A delay measurement through the group (urltest.URLTest: an outer group's check,
// the clash API) takes no part in the self-heal: no wrapper — the measurement's
// start is not reset after the open — no counts, no retry, no pending failure, no
// check.
func TestRRTrack_delayMeasurementUnwatched(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.s.balancer.stickyHash = nil
	if _, err := urltest.URLTest(context.Background(), f.g.link, f.s); err != nil {
		t.Fatal("the measurement through the group answers: ", err)
	}
	conn, err := f.s.DialContext(urltest.WithMeasureLx(context.Background()), N.NetworkTCP, siteAddr("ok.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := common.Cast[*rrFirstByteConn](conn); wrapped || N.NeedHandshakeForWrite(conn) {
		t.Fatal("a measurement is not wrapped and declares no write handshake")
	}
	conn.Close()
	f.s.balancer.counter.Store(0) // the next pick is a
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	if _, err := f.s.DialContext(urltest.WithMeasureLx(context.Background()), N.NetworkTCP, siteAddr("blocked.example")); err == nil {
		t.Fatal("a failed measurement shows the failure: no retry")
	}
	time.Sleep(20 * time.Millisecond)
	m := &f.s.balancer.memory
	m.access.Lock()
	counted := len(m.counts)
	if f.s.balancer.trackOf("a").proved.Load() != math.MinInt64 {
		counted++ // a proof mark from a measurement
	}
	m.access.Unlock()
	if counted != 0 || f.dials("b")+f.dials("c") > 1 || f.s.balancer.trackOf("a").pendingN.Load() != 0 {
		t.Fatalf("a measurement records nothing and gets no retry: counted %d, b %d c %d", counted, f.dials("b"), f.dials("c"))
	}
	if f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastCheckEnd.Load() != nil {
		t.Fatal("a measurement starts no check")
	}
}

// Only a connection started after a failure proves its node alive: a byte read
// after the failure by a connection picked before it (a byte that waited in a
// buffer) settles nothing, and the node's byte mark keeps the start.
func TestRRPending_proofByConnectionStart(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	ctx := f.keyOn(t, "blocked.example", 0)
	track := f.s.balancer.trackOf("a")
	early := time.Now()
	time.Sleep(time.Millisecond)
	at := rrClock(time.Now())
	track.addPending(rrPending{failure: f.s.rrNewFailure(f.pickAt(ctx, "blocked.example", early), "a"), at: at})
	time.Sleep(time.Millisecond)
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "a", track: track, start: early})
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 0 || track.pendingN.Load() != 1 {
		t.Fatal("a connection picked before the failure proves nothing about the time after it")
	}
	if f.s.balancer.rrByteSince("a", at) {
		t.Fatal("the node's byte mark is the start of the connection")
	}
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "a", track: track, start: time.Now()})
	if shift, _ := f.shift(ctx, "blocked.example"); shift != 1 || track.pendingN.Load() != 0 {
		t.Fatal("a connection started after the failure settles it")
	}
}

// The copy closes the connection at the client's end of stream when the node's
// connection has no half-close: that close is no client giving up.
func TestRRTrack_clientEOFCloseIsNoReason(t *testing.T) {
	for _, viaRoute := range []bool{false, true} {
		f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
		client, server := net.Pipe() // no half-close below
		go func() {
			buffer := make([]byte, 8)
			server.Read(buffer)
		}()
		t.Cleanup(func() { server.Close() })
		conn := newRRFirstByteConn(client, &rrConnWatch{s: f.s, member: "a", track: f.s.balancer.trackOf("a"), start: time.Now().Add(-rrHangAfter - time.Second)})
		conn.Write([]byte("hi"))
		if viaRoute {
			conn.LxClientFinished(false) // route/conn.go before destination.Close()
			conn.Close()
		} else {
			conn.CloseWrite() // the copy's half-close while the wrapper still takes writes
		}
		time.Sleep(20 * time.Millisecond)
		if f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastHangEnd.Load() != nil || f.probes("a") != 0 {
			t.Fatalf("viaRoute=%v: a close at the client's end of stream is no reason", viaRoute)
		}
	}
}

// A tracked dial whose selector turned to a direct leaf meanwhile is still wrapped,
// and its context is released on the close.
func TestRRTrack_trackedDialAlwaysReleased(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	track := f.s.balancer.trackOf("a")
	dialCtx, cancel := context.WithCancelCause(context.Background())
	dial := &rrDial{track: track, ctx: dialCtx, cancel: cancel}
	track.track(&track.dials, &dial.entry, rrClock(time.Now()), dial)
	client, server := net.Pipe()
	defer server.Close()
	conn := f.s.rrWatch(context.Background(), N.NetworkTCP, f.nodes["a"], client, rrPick{start: time.Now(), track: track, dial: dial}, "", nil)
	if _, wrapped := conn.(*rrFirstByteConn); !wrapped {
		t.Fatal("a tracked dial is wrapped whatever its leaf turned to")
	}
	if dialCtx.Err() != nil || track.dials.oldest.Load() != 0 {
		t.Fatal("open: untracked, context alive")
	}
	conn.Close()
	if dialCtx.Err() == nil {
		t.Fatal("the close releases the dial's context")
	}
}

// A dial that fails on its own between the dead verdict taking it off the list and
// its cancel still knows the verdict.
func TestRRTrack_killMarksDialUnderLock(t *testing.T) {
	track := newRRTrack("a")
	dialCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	dial := &rrDial{track: track, ctx: dialCtx, cancel: func(error) {}} // the cancel has not come yet
	track.track(&track.dials, &dial.entry, rrClock(time.Now()), dial)
	track.kill()
	dial.cancel = cancel
	if !dial.failed() {
		t.Fatal("a dial taken off by the dead verdict is struck, cancelled or not")
	}
	other := &rrDial{track: track, ctx: dialCtx, cancel: func(error) {}}
	track.track(&track.dials, &other.entry, rrClock(time.Now()), other)
	if other.failed() {
		t.Fatal("a dial that failed on its own is not struck")
	}
}

// lx:end SPEC 138
