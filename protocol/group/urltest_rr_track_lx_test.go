// lx:begin SPEC 138 round_robin unfinished connections and Q1 signs tests

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

// provedAny reports whether a first byte or an answer proved any pool node alive.
func provedAny(b *balancer) bool {
	for _, entry := range b.slotsSnapshot() {
		if entry.track != nil && entry.track.proved.Load() != math.MinInt64 {
			return true
		}
	}
	return false
}

// ageTrack moves every unfinished connection of track d into the past.
func ageTrack(track *rrTrack, d time.Duration) {
	track.access.Lock()
	defer track.access.Unlock()
	for _, list := range []*rrTrackList{&track.dials, &track.waits, &track.pwaits} {
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
	if f.s.balancer.heal.roundBusy() || f.probes("a") != 0 {
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
	if f.probes("a") != 3 { // S2's life probe, the first and the decisive probe of the check
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

// The tracker goes with its node: a node keeps it across slot changes, a node that
// leaves takes its Q1 signs with it, a newcomer starts empty.
func TestRRTrack_followsNode(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	b := f.s.balancer
	a, bTrack := b.trackOf("a"), b.trackOf("b")
	a.addPending(&rrSign{node: "a", b: rrClock(time.Now())})
	b.setSlots([]string{"d", "b", "c"})
	if b.trackOf("a") != nil || a.pendingN.Load() != 0 || len(a.pending) != 0 {
		t.Fatal("a node out of the pool takes its Q1 signs with it")
	}
	if b.trackOf("b") != bTrack || b.trackOf("d") == nil {
		t.Fatal("a node that stays keeps its tracker; a newcomer gets one")
	}
	b.setSlots([]string{"a", "b", "c"})
	if fresh := b.trackOf("a"); fresh == nil || fresh == a {
		t.Fatal("a node back in the pool starts a fresh tracker")
	}
	c := b.trackOf("c")
	c.addPending(&rrSign{node: "c", b: rrClock(time.Now())})
	if !b.replaceSlot("c", "d") || c.pendingN.Load() != 0 || b.trackOf("d") == nil {
		t.Fatal("a replaced node takes its Q1 signs with it")
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
	if track.pwaits.oldest.Load() == 0 || track.waits.oldest.Load() != 0 {
		t.Fatal("a flow that sent waits for an answer, among the UDP flows")
	}
	if _, _, err := echo.ReadFrom(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if track.pwaits.oldest.Load() != 0 {
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
	if allocs := testing.AllocsPerRun(1000, func() { f.s.balancer.pickKeyed(ctx, N.NetworkTCP, destination, nil, resolve) }); allocs != 0 {
		t.Fatalf("pick with a young unfinished connection allocates %v", allocs)
	}
	ageTrack(track, rrHangAfter+time.Second)
	track.lookAt.Store(rrClock(time.Now().Add(time.Hour)))
	if allocs := testing.AllocsPerRun(1000, func() { f.s.balancer.pickKeyed(ctx, N.NetworkTCP, destination, nil, resolve) }); allocs != 0 {
		t.Fatalf("pick with an old unfinished connection looked at lately allocates %v", allocs)
	}
}

type nopKill struct{}

func (nopKill) rrKill() {}

// --- III: a failure waits for the verdict on its node -----------------------------

// The pick hot path with the trackers and the pick hook wired: an IP destination,
// a non-empty shift table, an unfinished connection younger than 5 s on the slot.
func BenchmarkRRPickTracked(b *testing.B) {
	balancer := &balancer{poolSize: 3, stickyHash: []string{C.URLTestStickyProcess, C.URLTestStickyDomain}}
	balancer.setSlots([]string{"a", "b", "c"})
	balancer.rr = allAlive{}
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
// start is not reset after the open — no counts, no retry, no Q1 sign, no
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
	if f.s.balancer.heal.roundBusy() || f.s.balancer.heal.pauseEnd() != nil {
		t.Fatal("a measurement starts no check")
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
	conn := f.s.rrWatch(context.Background(), N.NetworkTCP, f.nodes["a"], client, rrPick{start: time.Now(), track: track, dial: dial}, nil, false)
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
