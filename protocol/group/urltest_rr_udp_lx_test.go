// lx:begin SPEC 138 round_robin self-heal for UDP tests (stage 3)

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
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// memPacketConn is an in-memory UDP flow: with echo every packet the client sends
// comes back; without it the destination stays silent.
type memPacketConn struct {
	echo   bool
	in     chan []byte
	closed chan struct{}
	once   sync.Once
}

func newMemPacketConn(echo bool) *memPacketConn {
	return &memPacketConn{echo: echo, in: make(chan []byte, 256), closed: make(chan struct{})}
}

var memAddr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 53}

func (c *memPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case packet := <-c.in:
		return copy(p, packet), memAddr, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *memPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if c.echo {
		c.feed(p)
	}
	return len(p), nil
}

// feed delivers a packet from the destination.
func (c *memPacketConn) feed(p []byte) {
	select {
	case c.in <- append([]byte(nil), p...):
	default:
	}
}

func (c *memPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *memPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}
func (c *memPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *memPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memPacketConn) SetWriteDeadline(time.Time) error { return nil }

// udpNode is a fake node that takes TCP and UDP (tcpOnly: TCP only). A probe goes
// to the group's test server through healNode; a UDP open is an in-memory flow:
//   - dead (healNode) or listenFail: the open fails; listenFail alone leaves the
//     probe answering;
//   - mute: the destination never answers; otherwise it echoes.
//
// A connected UDP flow (DialContext "udp") is a net.Pipe whose far end reads the
// client's packet and echoes it, or closes without an answer with mute.
type udpNode struct {
	*healNode
	typ        string
	tcpOnly    bool
	listenFail atomic.Bool
	mute       atomic.Bool
	listens    atomic.Int32
}

func (n *udpNode) Type() string { return n.typ }

func (n *udpNode) Network() []string {
	if n.tcpOnly {
		return []string{N.NetworkTCP}
	}
	return []string{N.NetworkTCP, N.NetworkUDP}
}

func (n *udpNode) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	n.listens.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.dead.Load() || n.listenFail.Load() {
		return nil, E.New("node ", n.tag, " cannot open UDP")
	}
	return newMemPacketConn(!n.mute.Load()), nil
}

func (n *udpNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkUDP {
		return n.healNode.DialContext(ctx, network, destination)
	}
	n.listens.Add(1)
	if n.dead.Load() || n.listenFail.Load() {
		return nil, E.New("node ", n.tag, " cannot open UDP")
	}
	client, server := net.Pipe()
	mute := n.mute.Load()
	go func() {
		defer server.Close()
		buffer := make([]byte, 64)
		count, err := server.Read(buffer)
		if err != nil || mute {
			return
		}
		_, _ = server.Write(buffer[:count])
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

type udpFixture struct {
	*healFixture
	udp map[string]*udpNode
}

// newUDPFixture is newHealFixture with udpNode nodes of the proxy type vless and no
// stickiness: the first pick is slot 0.
func newUDPFixture(t *testing.T, tags []string, pool []string) *udpFixture {
	t.Helper()
	f := newHealFixture(t, tags, pool, 0)
	manager := f.g.outbound.(*fakeManager)
	udp := make(map[string]*udpNode, len(tags))
	for i, tag := range tags {
		node := &udpNode{healNode: f.nodes[tag], typ: C.TypeVLESS}
		udp[tag] = node
		manager.byTag[tag] = node
		f.g.outbounds[i] = node
	}
	f.s.balancer.stickyHash = nil
	return &udpFixture{healFixture: f, udp: udp}
}

func udpSite() M.Socksaddr { return M.ParseSocksaddrHostPort("example.org", 443) }

func (f *udpFixture) listen(ctx context.Context) (net.PacketConn, error) {
	return f.s.ListenPacket(ctx, udpSite())
}

func (f *udpFixture) listens(tag string) int32 { return f.udp[tag].listens.Load() }

// exchange sends one packet and waits for the echo.
func exchange(t *testing.T, conn net.PacketConn) {
	t.Helper()
	if _, err := conn.WriteTo([]byte("hi"), memAddr); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if n, _, err := conn.ReadFrom(buffer); err != nil || n == 0 {
		t.Fatal("precondition: an answer: ", err)
	}
}

// keyOn returns the context of a connection to example.org whose sticky slot is slot.
func (f *udpFixture) keyOn(t *testing.T, slot int) context.Context {
	t.Helper()
	n := uint64(len(f.pool()))
	for i := 0; i < 10000; i++ {
		metadata := &adapter.InboundContext{Domain: "example.org", ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/app/" + strconv.Itoa(i)}}
		ctx := adapter.WithContext(context.Background(), metadata)
		if hashKey(f.s.balancer.stickyKey(ctx, udpSite()))%n == uint64(slot) {
			return ctx
		}
	}
	t.Fatal("no key for slot ", slot)
	return nil
}

func (f *udpFixture) counts(tag string) (conns uint32, fails uint32) {
	m := &f.s.balancer.memory
	m.access.Lock()
	defer m.access.Unlock()
	if c := m.counts[tag]; c != nil {
		return c.conns, c.fails
	}
	return 0, 0
}

func (f *udpFixture) checkStarted() bool {
	heal := &f.s.balancer.heal
	return heal.roundBusy() || heal.pauseEnd() != nil
}

// --- the shared pool ----------------------------------------------------------------

// UDP picks from the same slots: a dead slot and a quarantined one are skipped.
func TestRRUDP_poolSkipsDeadAndQuarantined(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.g.history.DeleteURLTestHistory("a")
	quarantine := map[string]bool{"c": true}
	f.s.balancer.memory.quarantine.Store(&quarantine)
	for i := 0; i < 6; i++ {
		conn, err := f.listen(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if f.listens("a") != 0 || f.listens("c") != 0 || f.listens("b") != 6 {
		t.Fatalf("UDP must skip the dead and the quarantined slot: a %d b %d c %d", f.listens("a"), f.listens("b"), f.listens("c"))
	}
}

// --- a failed open ------------------------------------------------------------------

// The retry skips a slot whose node does not take UDP; a node that answers its
// probe stays.
func TestRRUDP_retrySkipsSlotWithoutUDP(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.udp["b"].tcpOnly = true
	f.udp["a"].listenFail.Store(true)
	conn, err := f.listen(context.Background())
	if err != nil {
		t.Fatal("the UDP flow must open through c: ", err)
	}
	conn.Close()
	if f.listens("b") != 0 || f.listens("c") != 1 {
		t.Fatalf("the retry goes to c, past the TCP-only b: b %d c %d", f.listens("b"), f.listens("c"))
	}
	f.waitCheck(t)
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a answers its probe: kept, got %v", f.pool())
	}
}

// A client cancel is no failure: no retry, no check.
func TestRRUDP_clientCancelNoRetry(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.listen(ctx); err == nil {
		t.Fatal("precondition: the open fails")
	}
	time.Sleep(20 * time.Millisecond)
	if f.listens("b") != 0 || f.listens("c") != 0 || f.checkStarted() {
		t.Fatal("a client cancel gets no retry and no check")
	}
}

// --- the first packet ---------------------------------------------------------------

// a does not open UDP but answers its probe; the retry through b gets the first
// packet: the network is marked alive, and nothing else is recorded — UDP shifts
// no key and stays out of the slot counters (no flow, no confirmed failure).
func TestRRUDP_firstPacketConfirmsRetryNoShift(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.s.balancer.stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
	f.udp["a"].listenFail.Store(true)
	ctx := f.keyOn(t, 0)
	conn, err := f.s.ListenPacket(ctx, udpSite())
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := common.Cast[*rrFirstPacketConn](conn); !wrapped {
		t.Fatal("the retry of a proxy node is watched")
	}
	exchange(t, conn)
	conn.Close()
	f.waitCheck(t)
	time.Sleep(20 * time.Millisecond)
	for _, tag := range []string{"a", "b"} {
		if conns, fails := f.counts(tag); conns != 0 || fails != 0 {
			t.Fatalf("UDP counts nothing for %s, got %d/%d", tag, conns, fails)
		}
	}
	if !provedAny(f.s.balancer) {
		t.Fatal("the first packet marks the network alive")
	}
	if size := f.s.balancer.memory.shifts.size.Load(); size != 0 {
		t.Fatalf("UDP shifts no key, the table holds %d", size)
	}
}

// UDP stays out of the key table: a UDP flow of a key that TCP shifted off a
// rides the shift (the pick is shared) but its first packet confirms no failure
// of the nodes the key passed.
func TestRRUDP_keyTableUntouched(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.s.balancer.stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
	ctx := f.keyOn(t, 0)
	// TCP shifted the key off a: the site was refused through live a.
	f.s.rrShiftOn(f.s.balancer.rrKeyOf(ctx, udpSite()), 0, "a", rrShiftK1)
	if shift := f.s.balancer.memory.shifts.shiftOf(f.s.balancer.rrKeyOf(ctx, udpSite()).table); shift != 1 {
		t.Fatalf("precondition: TCP shifted the key by 1, got %d", shift)
	}
	packetConn, err := f.s.ListenPacket(ctx, udpSite())
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, packetConn)
	packetConn.Close()
	if f.listens("a") != 0 || f.listens("b") != 1 {
		t.Fatal("the UDP flow rides the shift to b")
	}
	time.Sleep(20 * time.Millisecond)
	if _, fails := f.counts("a"); fails != 0 {
		t.Fatalf("a UDP packet confirms no failure through the key, got %d", fails)
	}
}

// A direct leaf is not wrapped: its open goes to no server and proves nothing.
func TestRRUDP_directLeafNotWrapped(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.udp["a"].typ = C.TypeDirect
	conn, err := f.listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, wrapped := common.Cast[*rrFirstPacketConn](conn); wrapped {
		t.Fatal("a direct leaf must not be wrapped")
	}
	if provedAny(f.s.balancer) {
		t.Fatal("the UDP open of a direct leaf is no first byte")
	}
	if conns, _ := f.counts("a"); conns != 0 {
		t.Fatalf("a UDP flow is not counted, got %d", conns)
	}
}

// --- packets went out, no answer came -----------------------------------------------

// --- a connected UDP flow (DialContext "udp") ----------------------------------------

// A connected UDP flow that does not open is retried as a UDP open; one whose
// destination closes without an answer is no failure (TCP would start a check):
// only the close after a long wait is a reason.
func TestRRUDP_connectedFlow(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.udp["a"].listenFail.Store(true)
	conn, err := f.s.DialContext(context.Background(), N.NetworkUDP, udpSite())
	if err != nil {
		t.Fatal("the connected flow must open through b: ", err)
	}
	conn.Close()
	if f.listens("b") != 1 {
		t.Fatal("the retry goes through b")
	}
	f.waitCheck(t)

	for _, start := range []time.Time{time.Now(), time.Now().Add(-rrHangAfter - time.Second)} {
		g := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
		g.udp["a"].mute.Store(true)
		lower, err := g.udp["a"].DialContext(context.Background(), N.NetworkUDP, udpSite())
		if err != nil {
			t.Fatal(err)
		}
		watchedConn := g.s.rrWatch(context.Background(), N.NetworkUDP, g.udp["a"], lower, rrPick{start: start}, nil, false)
		if _, wrapped := watchedConn.(*rrFirstByteConn); !wrapped {
			t.Fatal("precondition: watched")
		}
		watchedConn.Write([]byte("hi"))
		buffer := make([]byte, 8)
		if n, _ := watchedConn.Read(buffer); n != 0 {
			t.Fatal("precondition: no answer")
		}
		time.Sleep(20 * time.Millisecond)
		if g.checkStarted() {
			t.Fatal("a read error of a UDP flow is no failure")
		}
		watchedConn.Close()
		time.Sleep(20 * time.Millisecond)
		if g.checkStarted() || g.probes("a") != 0 {
			t.Fatal("the close from our side is CANCEL: nothing follows")
		}
	}
}

// --- the fast paths of the packet copy ----------------------------------------------

// seqPacketConn hands out a fixed sequence of packets, then io.EOF; its own packet
// read waiter counts how many times the copy asks for it, and it offers a batch
// read waiter and a batch writer of its own.
type seqPacketConn struct {
	*memPacketConn
	access  sync.Mutex
	packets [][]byte
	creates atomic.Int32
	batches atomic.Int32
	need    bool // NeedHandshakeForWrite of the node's own connection
}

func (c *seqPacketConn) next() ([]byte, bool) {
	c.access.Lock()
	defer c.access.Unlock()
	if len(c.packets) == 0 {
		return nil, false
	}
	packet := c.packets[0]
	c.packets = c.packets[1:]
	return packet, true
}

func (c *seqPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	packet, ok := c.next()
	if !ok {
		return 0, nil, io.EOF
	}
	return copy(p, packet), memAddr, nil
}

func (c *seqPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	packet, ok := c.next()
	if !ok {
		return M.Socksaddr{}, io.EOF
	}
	buffer.Write(packet)
	return M.SocksaddrFromNet(memAddr), nil
}

func (c *seqPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

func (c *seqPacketConn) CreateReadWaiter() (N.PacketReadWaiter, bool) {
	c.creates.Add(1)
	return &seqWaiter{conn: c}, true
}

func (c *seqPacketConn) CreatePacketBatchReadWaiter() (N.PacketBatchReadWaiter, bool) {
	c.batches.Add(1)
	return nil, false
}

func (c *seqPacketConn) CreatePacketBatchWriter() (N.PacketBatchWriter, bool) {
	c.batches.Add(1)
	return nil, false
}

func (c *seqPacketConn) NeedHandshakeForWrite() bool { return c.need }

type seqWaiter struct {
	conn    *seqPacketConn
	options N.ReadWaitOptions
}

func (w *seqWaiter) InitializeReadWaiter(options N.ReadWaitOptions) bool {
	w.options = options
	return false
}

func (w *seqWaiter) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	packet, ok := w.conn.next()
	if !ok {
		return nil, M.Socksaddr{}, io.EOF
	}
	buffer := w.options.NewPacketBuffer()
	buffer.Write(packet)
	return buffer, M.SocksaddrFromNet(memAddr), nil
}

// collector is the client side of the copy: it keeps what it receives.
type collector struct {
	packets [][]byte
}

func (c *collector) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	c.packets = append(c.packets, append([]byte(nil), buffer.Bytes()...))
	buffer.Release()
	return nil
}

// The download copy of sing reads through the packet read waiter of the node's
// connection with the wrapper peeking at the first packet; the first packet
// completes the "handshake", the copy re-selects its path and gets the bare waiter,
// and from then on the batch read path of the node's connection is reachable
// through the wrapper. The packets arrive intact.
func TestRRUDP_copyReselectsReadPath(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	lower := &seqPacketConn{memPacketConn: newMemPacketConn(false)}
	for i := 0; i < 100; i++ {
		lower.packets = append(lower.packets, []byte("packet "+strconv.Itoa(i)))
	}
	conn := newRRFirstPacketConn(lower, &rrConnWatch{s: f.s, member: "a", start: time.Now(), packet: true})
	outer := f.g.interruptGroup.NewPacketConn(conn, false)
	if !N.NeedHandshakeForReadAny(outer) || conn.ReaderReplaceable() {
		t.Fatal("before the first packet: read handshake pending, no bypass")
	}
	if _, reached := bufio.CreatePacketBatchReadWaiter(bufio.NewPacketConn(outer)); reached || lower.batches.Load() != 0 {
		t.Fatal("before the first packet the batch read path is not reachable through the wrapper")
	}
	var received collector
	if _, err := bufio.CopyPacket(&received, bufio.NewPacketConn(outer)); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if len(received.packets) != 100 || string(received.packets[99]) != "packet 99" {
		t.Fatalf("the packets must arrive intact: %d", len(received.packets))
	}
	if lower.creates.Load() != 2 {
		t.Fatalf("the copy asks for the read waiter before and after the first packet, got %d", lower.creates.Load())
	}
	if lower.batches.Load() == 0 {
		t.Fatal("after the first packet the copy reaches the batch read path of the node's connection")
	}
	if !provedAny(f.s.balancer) {
		t.Fatal("the first packet seen by the peeking waiter")
	}
	if N.NeedHandshakeForReadAny(outer) || !conn.ReaderReplaceable() {
		t.Fatal("after the first packet: no read handshake, reads bypass the wrapper")
	}
	if waiter, _ := conn.CreateReadWaiter(); waiter != nil {
		if _, peeking := waiter.(*rrFirstPacketReadWaiter); peeking {
			t.Fatal("after the first packet the waiter is the node's own")
		}
	}
}

// The first packet the client sends goes through the wrapper; then the upload copy
// sees the write handshake complete and reaches the batch writer of the node's
// connection. A write handshake of the node's own connection shows through.
func TestRRUDP_writePathAfterFirstPacket(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	for _, need := range []bool{false, true} {
		lower := &seqPacketConn{memPacketConn: newMemPacketConn(false), need: need}
		conn := newRRFirstPacketConn(lower, &rrConnWatch{s: f.s, member: "a", start: time.Now(), packet: true})
		outer := bufio.NewPacketConn(f.g.interruptGroup.NewPacketConn(conn, false))
		client := newMemPacketConn(false)
		handshake := N.NewPacketHandshakeState(bufio.NewPacketConn(client), outer)
		if !handshake.Upgradable() || conn.WriterReplaceable() {
			t.Fatal("before the first packet sent: write handshake pending, writes through the wrapper")
		}
		if _, reached := bufio.CreatePacketBatchWriter(outer); reached || lower.batches.Load() != 0 {
			t.Fatal("before the first packet the batch writer is not reachable through the wrapper")
		}
		buffer := buf.NewPacket()
		buffer.WriteString("hi")
		if err := outer.WritePacket(buffer, M.SocksaddrFromNet(memAddr)); err != nil {
			t.Fatal(err)
		}
		if completed := handshake.Check() != nil; completed == need {
			t.Fatalf("after the first packet sent the copy re-selects its write path unless the node needs a handshake (need %v)", need)
		}
		if N.NeedHandshakeForWriteAny(outer) != need || !conn.WriterReplaceable() {
			t.Fatal("after the first packet sent: the node's own write handshake only, writes may bypass the wrapper")
		}
		bufio.CreatePacketBatchWriter(outer)
		if lower.batches.Load() == 0 {
			t.Fatal("after the first packet sent the batch writer of the node's connection is reachable")
		}
	}
}

// --- concurrency --------------------------------------------------------------------

// Parallel UDP flows with failing opens, answers and silent closes while the group
// interrupts its flows: no race, no deadlock.
func TestRRUDP_concurrent(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.udp["a"].listenFail.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				conn, err := f.listen(context.Background())
				if err != nil {
					continue
				}
				conn.WriteTo([]byte("hi"), memAddr)
				if j%2 == 0 {
					buffer := make([]byte, 8)
					conn.ReadFrom(buffer)
				}
				conn.Close()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			f.g.interruptGroup.Interrupt(true)
		}
	}()
	wg.Wait()
	f.waitCheck(t)
}

// --- review fixes ---------------------------------------------------------------------

// A K5 mark learned by TCP does not stop the retry of a failed UDP open (UDP has
// no key), and the node gets its life probe (S1).
func TestRRUDP_markedKeyStillRetried(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.s.balancer.stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
	ctx := f.keyOn(t, 0)
	table := &f.s.balancer.memory.shifts
	key := f.s.balancer.rrKeyOf(ctx, udpSite())
	table.access.Lock()
	table.entry(key.table).marked = true
	table.access.Unlock()
	f.udp["a"].listenFail.Store(true)
	conn, err := f.s.ListenPacket(ctx, udpSite())
	if err != nil {
		t.Fatal("the failed UDP open of a marked key is retried: ", err)
	}
	conn.Close()
	f.waitCheck(t)
	if f.probes("a") != 1 {
		t.Fatal("and its node gets the life probe (S1)")
	}
}

// A packet that did not go out is no "client sent".
func TestRRUDP_failedWriteIsNotSent(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	lower := newMemPacketConn(false)
	lower.Close()
	conn := f.s.rrWatchPacket(context.Background(), f.udp["a"], lower, rrPick{start: time.Now().Add(-rrHangAfter - time.Second)})
	if _, err := conn.WriteTo([]byte("hi"), memAddr); err == nil {
		t.Fatal("precondition: the write fails")
	}
	conn.Close()
	time.Sleep(20 * time.Millisecond)
	if f.checkStarted() {
		t.Fatal("nothing went out: no reason")
	}
}
