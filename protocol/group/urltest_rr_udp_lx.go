package group

// lx:begin SPEC 138 round_robin self-heal for UDP (stage 3)
//
// UDP takes its node from the same slots as TCP (pick), so a dead or quarantined
// node, and a slot under suspicion, is skipped for UDP too. On top of that (§3.8):
//
//   - An open that fails (ListenPacket of the node returned an error, a client
//     cancel aside) is handled as a failed TCP dial (rrHealRetry): one retry
//     through the next live slot that takes UDP and is no nested urltest, and the
//     verdict on the node as for TCP (the check with a control, or a verdict probe
//     of a direct or endpoint leaf) with the same limits, gaps and freeze. It puts the node under suspicion, not the address: a key that TCP
//     marked "address problem" is still retried and checked. A UDP failure shifts
//     no key. The control of the check needs TCP only (the probe is TCP); the
//     retry needs UDP.
//   - "Packets went out, no answer came": from the client's first packet until the
//     first packet back (or the close) the flow is an unfinished connection of its
//     node (urltest_rr_track_lx.go): a pick of the slot that finds it older than
//     rrHangAfter, or its close from our side after that long, is a reason for a
//     check, and the dead verdict closes it. One-way UDP is legal, so this is only a
//     reason for a check (rrHang): no retry, no key shift, no failure counted; the probe decides
//     (dead with an answering control → struck out and replaced; nobody answers →
//     own network; it answers → nothing). Such flows are common, so rrHang drops
//     the reason for a node proved alive since the flow started or answering a
//     probe lately, and its checks keep their own gap. An interrupt of the group
//     (rrInterrupting), its Close and a connection test are no reason. A packet
//     counts as sent once its write succeeded.
//   - The first packet received is the first byte: it moves the node's proof mark
//     (rrTrack.proved) to the start of the flow. UDP stays out of the slot counters
//     and the quarantine: no flow counted, no failure confirmed by a UDP retry.
//
// The watch is a thin wrapper of the node's packet connection, under the group's
// interrupt wrapper, that keeps the packet fast paths of sing (common/bufio
// CopyPacket):
//   - reads: before the first packet it hands the copy the packet read waiter of
//     the node's connection, peeking at each packet (CreateReadWaiter), declares
//     NeedHandshakeForRead and is not ReaderReplaceable, so the copy takes no
//     batch read path through it. The first packet completes the "handshake": the
//     copy's transfer hook (packetCopySession.Transfer → HandshakeState.Check)
//     returns ErrHandshakeCompleted, the copy re-selects its path, and from then
//     on the wrapper is ReaderReplaceable: batch, connected-batch and syscall read
//     waiters of the node's connection are reached through Upstream;
//   - writes: the first packet the client sends goes through the wrapper, which
//     declares NeedHandshakeForWrite until then; once a packet went out the write
//     handshake is complete for the upload copy, which re-selects its path, and the
//     wrapper is WriterReplaceable: batch writers of the node's connection are
//     reached through Upstream. A write handshake of the node's own connection
//     is reported through the wrapper as without it.
//
// Not wrapped: a connection test (SPEC 139), a pick outside the pool, a nested
// urltest (it watches its own flows), and a direct leaf — its open goes to no
// server and proves nothing, and the wrapper would cost nothing but the copy's
// fast path. A connected UDP flow (DialContext "udp") is watched by the stream
// wrapper in its UDP mode (rrWatch).

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// rrHealListen handles a failed UDP open of a round_robin group as rrHealDial does
// a failed dial: the background check, and the packet connection of the retry
// through the next live slot that takes UDP. ok is false when there is no retry or
// it failed too: the caller returns the error of the first node.
func (s *URLTest) rrHealListen(ctx context.Context, destination M.Socksaddr, failed adapter.Outbound, listenErr error, picked rrPick) (net.PacketConn, bool) {
	struck := picked.dial.failed()
	var conn net.PacketConn
	retry, ok := s.rrHealRetry(ctx, N.NetworkUDP, failed, listenErr, picked, struck, func(ctx context.Context, dialer adapter.Outbound) error {
		var err error
		conn, err = dialer.ListenPacket(ctx, destination)
		return err
	})
	if !ok {
		return nil, false
	}
	// The first packet of the retry confirms the failure of the first node.
	picked.dial = retry.dial
	return s.rrWatchPacketFor(ctx, retry.dialer, conn, picked, retry.confirmFor, retry.failure), true
}

// rrWatchPacket wraps an opened packet connection of a round_robin group to watch
// for its first packet (ListenPacket). It returns conn as is when there is nothing
// to watch.
func (s *URLTest) rrWatchPacket(ctx context.Context, dialed adapter.Outbound, conn net.PacketConn, picked rrPick) net.PacketConn {
	if s.balancer == nil {
		return conn
	}
	return s.rrWatchPacketFor(ctx, dialed, conn, picked, "", nil)
}

func (s *URLTest) rrWatchPacketFor(ctx context.Context, dialed adapter.Outbound, conn net.PacketConn, picked rrPick, confirmFor string, failure *rrFailure) net.PacketConn {
	picked.dial.opened()
	watch, ok, direct := s.rrNewWatch(ctx, dialed, picked, confirmFor, failure, true)
	if !ok || direct {
		picked.dial.release() // not reached for a tracked open (rrNewWatch); kept for every path
		return conn
	}
	wrapped := newRRFirstPacketConn(conn, &watch)
	wrapped.dial = picked.dial
	return wrapped
}

// rrFirstPacketConn watches a UDP flow for its first packet (see the file header).
type rrFirstPacketConn struct {
	N.NetPacketConn
	state atomic.Uint32 // rrStateWrote, rrStateGotByte, rrStateClosed, rrStateTracked
	watch *rrConnWatch  // = &held
	held  rrConnWatch
	entry rrTrackEntry // in watch.track.waits from the first packet sent to the first one received
	dial  *rrDial      // the tracked open of the flow; its context is released on the close
}

func newRRFirstPacketConn(conn net.PacketConn, watch *rrConnWatch) *rrFirstPacketConn {
	c := &rrFirstPacketConn{NetPacketConn: bufio.NewPacketConn(conn), held: *watch}
	c.watch = &c.held
	return c
}

func (c *rrFirstPacketConn) set(bit uint32) uint32 {
	for {
		old := c.state.Load()
		if old&bit == bit || c.state.CompareAndSwap(old, old|bit) {
			return old
		}
	}
}

// received takes one packet read before the first one was seen.
func (c *rrFirstPacketConn) received(err error) {
	if err != nil {
		return // no packet: a read error is no failure for UDP, the close decides
	}
	old := c.set(rrStateGotByte)
	c.untrack()
	if old&rrStateGotByte == 0 {
		c.watch.s.rrFirstByte(c.watch)
	}
}

// sent takes a packet the client sent; the first one lists the flow as waiting for
// an answer in the tracker of its node.
func (c *rrFirstPacketConn) sent() {
	if c.state.Load()&rrStateWrote != 0 {
		return
	}
	old := c.set(rrStateWrote)
	if old&(rrStateWrote|rrStateGotByte|rrStateClosed) != 0 || c.watch.track == nil {
		return
	}
	c.watch.track.track(&c.watch.track.pwaits, &c.entry, rrClock(time.Now()), c)
	c.set(rrStateTracked)
	if c.state.Load()&(rrStateGotByte|rrStateClosed) != 0 {
		c.watch.track.untrack(&c.entry)
	}
}

func (c *rrFirstPacketConn) untrack() {
	if c.state.Load()&rrStateTracked != 0 {
		c.watch.track.untrack(&c.entry)
	}
}

// rrKill is the dead verdict on the node: the flow waits for an answer that will
// not come; it is closed.
func (c *rrFirstPacketConn) rrKill() {
	c.set(rrStateClosed)
	_ = c.NetPacketConn.Close()
}

func (c *rrFirstPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if c.state.Load()&rrStateGotByte != 0 {
		return c.NetPacketConn.ReadPacket(buffer)
	}
	destination, err := c.NetPacketConn.ReadPacket(buffer)
	c.received(err)
	return destination, err
}

func (c *rrFirstPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.state.Load()&rrStateGotByte != 0 {
		return c.NetPacketConn.ReadFrom(p)
	}
	n, addr, err := c.NetPacketConn.ReadFrom(p)
	c.received(err)
	return n, addr, err
}

func (c *rrFirstPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	err := c.NetPacketConn.WritePacket(buffer, destination)
	if err == nil {
		c.sent()
	}
	return err
}

func (c *rrFirstPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.NetPacketConn.WriteTo(p, addr)
	if err == nil {
		c.sent()
	}
	return n, err
}

// Close from our side with packets sent and none received: after a long wait a
// reason for a check (rrHang skips an interrupt of the group and its Close).
func (c *rrFirstPacketConn) Close() error {
	old := c.set(rrStateClosed)
	c.untrack()
	if old&(rrStateClosed|rrStateGotByte) == 0 && old&rrStateWrote != 0 {
		c.watch.s.rrHang(c.watch.member, c.watch.start, true)
	}
	err := c.NetPacketConn.Close()
	c.dial.release()
	return err
}

// NeedHandshakeForRead holds until the first packet: then the copy re-selects its
// read path.
func (c *rrFirstPacketConn) NeedHandshakeForRead() bool {
	return c.state.Load()&rrStateGotByte == 0 || N.NeedHandshakeForReadAny(c.NetPacketConn)
}

// NeedHandshakeForWrite holds until the client sent a packet: then the upload copy
// re-selects its write path.
func (c *rrFirstPacketConn) NeedHandshakeForWrite() bool {
	return c.state.Load()&rrStateWrote == 0 || N.NeedHandshakeForWriteAny(c.NetPacketConn)
}

// CreateReadWaiter hands out the packet read waiter of the node's connection;
// before the first packet it peeks at each packet.
func (c *rrFirstPacketConn) CreateReadWaiter() (N.PacketReadWaiter, bool) {
	waiter, created := bufio.CreatePacketReadWaiter(c.NetPacketConn)
	if !created {
		return nil, false
	}
	if c.state.Load()&rrStateGotByte != 0 {
		return waiter, true
	}
	return &rrFirstPacketReadWaiter{PacketReadWaiter: waiter, conn: c}, true
}

func (c *rrFirstPacketConn) ReaderReplaceable() bool {
	return c.state.Load()&rrStateGotByte != 0
}

func (c *rrFirstPacketConn) WriterReplaceable() bool {
	return c.state.Load()&rrStateWrote != 0
}

func (c *rrFirstPacketConn) Upstream() any {
	return c.NetPacketConn
}

type rrFirstPacketReadWaiter struct {
	N.PacketReadWaiter
	conn *rrFirstPacketConn
}

func (w *rrFirstPacketReadWaiter) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	buffer, destination, err := w.PacketReadWaiter.WaitReadPacket()
	if w.conn.state.Load()&rrStateGotByte == 0 {
		w.conn.received(err)
	}
	return buffer, destination, err
}

// lx:end SPEC 138
