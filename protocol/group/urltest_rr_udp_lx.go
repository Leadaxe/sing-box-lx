package group

// lx:begin SPEC 138 round_robin self-heal for UDP
//
// UDP takes its node from the same slots as TCP (pick), so a dead or quarantined
// node, and a slot under suspicion, is skipped for UDP too. On top of that:
//
//   - An open that fails is a failed open (rrHealRetry): one retry through the
//     next usable slot that takes UDP, S1 for an active failure; a timeout of the
//     open is a UDP wait (no suspicion for TCP, V9). UDP shifts no key, writes no
//     K2 memory and stays out of the slot counters.
//   - "Packets went out, no answer came": from the client's first packet until the
//     first packet back (or the close) the flow is a UDP wait of its node
//     (urltest_rr_track_lx.go). A look that finds one older than H starts a life
//     probe of the node, at most one per group every rrUDPLifeGap, which sets no
//     suspicion; its failure starts the check. D1 closes the flow. A packet counts
//     as sent once its write succeeded; the close is CANCEL.
//   - The first packet received is the ANSWER: a proof via the node.

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
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// rrHealListen handles a failed UDP open of a round_robin group as rrHealDial does
// a failed dial, with the packet connection of the retry through the next usable
// slot that takes UDP. ok is false when there is no retry or
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
	picked.dial, picked.start = retry.dial, retry.start
	return s.rrWatchPacketFor(ctx, retry.dialer, conn, picked, true), true
}

// rrWatchPacket wraps an opened packet connection of a round_robin group to watch
// for its first packet (ListenPacket). It returns conn as is when there is nothing
// to watch.
func (s *URLTest) rrWatchPacket(ctx context.Context, dialed adapter.Outbound, conn net.PacketConn, picked rrPick) net.PacketConn {
	if s.balancer == nil {
		return conn
	}
	return s.rrWatchPacketFor(ctx, dialed, conn, picked, false)
}

func (s *URLTest) rrWatchPacketFor(ctx context.Context, dialed adapter.Outbound, conn net.PacketConn, picked rrPick, retry bool) net.PacketConn {
	picked.dial.opened()
	watch, ok, direct := s.rrNewWatch(ctx, dialed, picked, nil, retry, true)
	if !ok || direct { // never for a tracked open (rrNewWatch): no context to release
		return conn
	}
	wrapped := newRRFirstPacketConn(conn, &watch)
	wrapped.dial = picked.dial
	return wrapped
}

// rrFirstPacketConn watches a UDP flow for its first packet (see the file header).
// Its state takes rrStateWrote, rrStateGotByte, rrStateClosed and rrStateTracked; it
// is in the pwaits of its node from the first packet sent to the first one received.
type rrFirstPacketConn struct {
	N.NetPacketConn
	rrWatched
}

func newRRFirstPacketConn(conn net.PacketConn, watch *rrConnWatch) *rrFirstPacketConn {
	c := &rrFirstPacketConn{NetPacketConn: bufio.NewPacketConn(conn)}
	c.hold(watch)
	return c
}

// received takes one packet read before the first one was seen.
func (c *rrFirstPacketConn) received(err error) {
	if err != nil {
		return // no packet: a read error is no failure for UDP, the close decides
	}
	old := c.set(rrStateGotByte)
	c.untrack()
	if old&rrStateGotByte == 0 {
		c.watch.s.rrFirstByte(c.watch, nil)
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

// rrKill is D1 on the node: the flow waits for an answer that will
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

// Close from our side is CANCEL: nothing follows.
func (c *rrFirstPacketConn) Close() error {
	c.set(rrStateClosed)
	c.untrack()
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
