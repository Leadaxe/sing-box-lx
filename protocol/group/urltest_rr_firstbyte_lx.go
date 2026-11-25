package group

// lx:begin SPEC 138 round_robin first byte
//
// An exchange of a round_robin connection ends with ANSWER at the first byte from
// the destination; an opened connection is no answer yet (ALGORITHM.md §2). It is
// an implicit REFUSE_DST when it ends with no byte although the client sent and did
// not close its side: VLESS, VMess, Trojan, Shadowsocks, a live mux session, HTTP/2
// transports, hysteria2 and TUIC hand out a connection before the destination
// answers. A close from our side — the client, an interrupt of the group, D1 — is
// CANCEL: nothing follows. No timer: SILENT is read at a look (rrInspect).
//
// The wrapper sits between the node's connection and the group's interrupt wrapper
// and costs the connection no fast path once both directions have started:
//   - reads: before the first byte it hands the copy the read waiter of the node's
//     connection, peeking at each buffer (CreateReadWaiter), and declares
//     NeedHandshakeForRead; with the first byte the copy's transfer hook sees the
//     handshake completed (N.HandshakeState.Check), re-selects its path, and from
//     then on gets the bare read waiter (or splice/vectorised reads: the wrapper
//     becomes ReaderReplaceable);
//   - writes: all the wrapper needs from them is the fact "the client sent
//     something". Until the first non-empty write it declares
//     NeedHandshakeForWrite, so the upload copy installs its transfer hook; after
//     that write the hook sees the handshake completed, the copy re-selects its
//     path, and the wrapper is WriterReplaceable: the node's writer (splice,
//     vectorised writes, ReadFrom) is reached through Upstream. A write handshake
//     of the node's own connection (VLESS/VMess/Trojan early data) is reported
//     through the wrapper as without it and keeps the copy on the wrapper until it
//     completes.
//   - kickWriteHandshake (route/conn.go) sees the declared write handshake and
//     nudges the connection before the copy starts. An empty nudge before the
//     client's first write reaches the node's connection only when that one has a
//     write handshake of its own — exactly when the nudge would reach it without
//     the wrapper — and never counts as the client sending. When the client
//     already has a payload cached (sniffed), the nudge writes it: the same bytes
//     the copy would write first, only from kickWriteHandshake.
//   - the client's end of stream reaches the node's connection past the wrapper
//     once it is WriterReplaceable (a half-close, or a close when the node's
//     connection has none); route/conn.go tells the wrapper about it
//     (LxClientFinished) when the wrapper lies on the write path it unwraps.
//
// Unfinished connections (urltest_rr_track_lx.go): from the client's first write
// until the first byte, a read error or the close, the connection is listed in the
// tracker of its node, and D1 on the node closes it. The dial of the connection
// (rrDial) lives with it: its context is released on the close.
//
// Not wrapped: a connection test (SPEC 139), a pick outside the pool, a nested
// urltest (it watches its own connections), and a direct leaf — an unreachable
// destination shows on its open, and the wrapper would cost it splice. Its open
// counts as the first byte.
//
// A connected UDP flow (DialContext "udp") takes the same wrapper in its UDP mode
// (urltest_rr_udp_lx.go): the first packet is the ANSWER, a read error is no
// outcome, and a flow sent with no answer is a UDP wait.

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

// rrClockBase is the origin of the monotonic marks of the self-heal (rrClock), a
// day before the start: a mark reckoned back from an early moment (a proof T before
// an answer) stays above 0, the E of no event.
var rrClockBase = time.Now().Add(-24 * time.Hour)

// rrClock returns the monotonic time since rrClockBase; never 0, which means "never".
func rrClock(at time.Time) int64 {
	return int64(at.Sub(rrClockBase)) + 1
}

// rrConnWatch is what a watched connection reports to its group.
type rrConnWatch struct {
	s      *URLTest
	member string    // the pool member the connection went through
	track  *rrTrack  // the tracker of member; nil when it holds no slot
	start  time.Time // the start of the connection (its pick)
	key    rrKey     // the shift-table key; invalid without stickiness and for UDP
	shift  int       // the shift of the key at the pick
	// sign is the Q1 sign of the first node that the first byte of this retry
	// completes; retry marks a retry, detour a pick moved off its slot by a
	// suspicion: a sign on either shifts no key (§4.4).
	sign   *rrSign
	retry  bool
	detour bool
	packet bool // a UDP flow: a read error is no outcome
}

// rrWatchConn wraps an opened TCP connection of a round_robin group to watch for
// its first byte (DialContext and the retry). It returns conn as is when there is
// nothing to watch; for a direct leaf the open itself is the first byte.
func (s *URLTest) rrWatchConn(ctx context.Context, network string, dialed adapter.Outbound, conn net.Conn, picked rrPick) net.Conn {
	if s.balancer == nil {
		return conn
	}
	return s.rrWatch(ctx, network, dialed, conn, picked, nil, false)
}

func (s *URLTest) rrWatch(ctx context.Context, network string, dialed adapter.Outbound, conn net.Conn, picked rrPick, sign *rrSign, retry bool) net.Conn {
	picked.dial.opened()
	var packet bool
	switch N.NetworkName(network) {
	case N.NetworkTCP:
	case N.NetworkUDP:
		packet = true // a connected UDP flow: a read error is no outcome
	default:
		picked.dial.release()
		return conn
	}
	watch, ok, direct := s.rrNewWatch(ctx, dialed, picked, sign, retry, packet)
	if !ok {
		picked.dial.release() // not reached for a tracked dial (rrNewWatch); kept for every path
		return conn
	}
	if direct {
		if !packet {
			s.rrFirstByte(&watch, nil)
		}
		return conn
	}
	wrapped := newRRFirstByteConn(conn, &watch)
	wrapped.dial = picked.dial
	return wrapped
}

// rrNewWatch counts a TCP connection opened through dialed and returns its watch; ok
// is false when there is nothing to watch: a connection test, a closed group, a pick outside
// the pool or a nested urltest. direct reports a direct leaf, which is not wrapped:
// for TCP its open is the first byte, for UDP the open proves nothing. A UDP watch
// carries no key: UDP shifts none (§4.4). A tracked dial (picked.dial) is always
// watched: its wrapper releases the dial's context on the close.
// The watch is a value: the wrapper holds it, so watching allocates nothing apart.
func (s *URLTest) rrNewWatch(ctx context.Context, dialed adapter.Outbound, picked rrPick, sign *rrSign, retry bool, packet bool) (rrConnWatch, bool, bool) {
	tracked := picked.dial != nil
	member := dialed.Tag() // a chain hop carries the tag of its member
	var track *rrTrack
	if tracked {
		// The dial of a pool member tracked at its pick: nothing to look up again.
		track = picked.dial.track
	} else {
		if rrUnwatched(ctx) || s.group.ctx.Err() != nil {
			return rrConnWatch{}, false, false
		}
		track = s.balancer.trackOf(member)
		if track == nil {
			return rrConnWatch{}, false, false // a fallback pick outside the pool (cold start)
		}
		node, loaded := s.outbound.Outbound(member)
		if !loaded || rrNestedURLTest(node) {
			return rrConnWatch{}, false, false
		}
	}
	if !packet {
		s.balancer.rrCountConn(member, picked.start) // Q1/Q2 conns; UDP stays out
	}
	watch := rrConnWatch{
		s:      s,
		member: member,
		track:  track,
		start:  picked.start,
		key:    picked.key,
		shift:  picked.shift,
		sign:   sign,
		retry:  retry,
		detour: picked.detour,
		packet: packet,
	}
	if packet {
		watch.key = rrKey{}
	}
	// A tracked dial is wrapped even if its selector turned to a direct leaf since
	// the dial started: the wrapper releases the dial's context on the close.
	return watch, true, !tracked && rrDirectLeaf(dialed)
}

// rrDirectLeaf reports whether node, unwrapped through selectors, is a direct
// outbound.
func rrDirectLeaf(node adapter.Outbound) bool {
	for {
		selector, isSelector := node.(*Selector)
		if !isSelector {
			return node.Type() == C.TypeDirect
		}
		node = selector.Selected(N.NetworkTCP)
		if node == nil {
			return false
		}
	}
}

const (
	rrStateWrote      uint32 = 1 << iota // the client sent a byte
	rrStateGotByte                       // the destination sent a byte
	rrStateClosed                        // closed from our side
	rrStateDone                          // the outcome is reported, or D1 closed it
	rrStateHalfClosed                    // the client closed its side: a close without a byte is no REFUSE_DST
	rrStateTracked                       // listed as waiting for its first byte (rrTrack.waits)
	rrStateSilent                        // SILENT given at a look (rrInspect)
	rrStateCounted                       // its SILENT counted by Q1
)

// rrFirstByteConn watches a connection for its first byte (see the file header).
type rrFirstByteConn struct {
	net.Conn
	state atomic.Uint32
	watch *rrConnWatch // = &held
	held  rrConnWatch
	entry rrTrackEntry // in watch.track.waits while the client waits for the first byte
	dial  *rrDial      // the tracked dial of the connection; its context is released on the close
	// wroteAt is w, the rrClock of the client's first write: the wait for the
	// first byte starts there.
	wroteAt atomic.Int64
	// silentShift is the shift K2 made on its SILENT, plus one; an ANSWER takes it
	// back (rrFirstByte).
	silentShift atomic.Int64
}

func newRRFirstByteConn(conn net.Conn, watch *rrConnWatch) *rrFirstByteConn {
	c := &rrFirstByteConn{Conn: conn, held: *watch}
	c.watch = &c.held
	return c
}

func (c *rrFirstByteConn) set(bit uint32) uint32 {
	for {
		old := c.state.Load()
		if old&bit == bit || c.state.CompareAndSwap(old, old|bit) {
			return old
		}
	}
}

// wrote takes the client's first non-empty write: the connection waits for its
// first byte from now on, listed in the tracker of its node.
func (c *rrFirstByteConn) wrote() {
	old := c.set(rrStateWrote)
	if old&(rrStateWrote|rrStateGotByte|rrStateClosed|rrStateDone) != 0 || c.watch.track == nil {
		return
	}
	list := &c.watch.track.waits
	if c.watch.packet {
		list = &c.watch.track.pwaits // a connected UDP flow: no suspicion, a reason only
	}
	at := rrClock(time.Now())
	c.wroteAt.Store(at)
	c.watch.track.track(list, &c.entry, at, c)
	c.set(rrStateTracked)
	if c.state.Load()&(rrStateGotByte|rrStateClosed|rrStateDone) != 0 {
		c.watch.track.untrack(&c.entry) // the outcome came in meanwhile
	}
}

// untrack takes the connection off the list of its node; set the outcome bit first.
func (c *rrFirstByteConn) untrack() {
	if c.state.Load()&rrStateTracked != 0 {
		c.watch.track.untrack(&c.entry)
	}
}

// rrKill is D1 on the node: the connection waits for a first byte that will not
// come; it is closed (CANCEL), and the client retries on its own.
func (c *rrFirstByteConn) rrKill() {
	c.set(rrStateDone | rrStateClosed)
	_ = c.Conn.Close()
}

// observe takes the outcome of one read before the first byte.
func (c *rrFirstByteConn) observe(n int, err error) {
	if n > 0 {
		old := c.set(rrStateGotByte | rrStateDone)
		c.untrack()
		if old&rrStateDone == 0 {
			c.watch.s.rrFirstByte(c.watch, c)
		}
		return
	}
	if err == nil || E.IsTimeout(err) || c.watch.packet {
		return // a read deadline of the consumer is no outcome: still watched; UDP waits for the close
	}
	old := c.set(rrStateDone)
	c.untrack()
	if old&(rrStateDone|rrStateGotByte|rrStateClosed|rrStateHalfClosed) != 0 || old&rrStateWrote == 0 {
		return // reported already, closed by us (CANCEL), half-closed, or the client sent nothing
	}
	c.watch.s.rrSilentClose(c)
}

func (c *rrFirstByteConn) Read(p []byte) (int, error) {
	if c.state.Load()&rrStateGotByte != 0 {
		return c.Conn.Read(p)
	}
	n, err := c.Conn.Read(p)
	c.observe(n, err)
	return n, err
}

func (c *rrFirstByteConn) ReadBuffer(buffer *buf.Buffer) error {
	if c.state.Load()&rrStateGotByte != 0 {
		return c.readBuffer(buffer)
	}
	start := buffer.Len()
	err := c.readBuffer(buffer)
	c.observe(buffer.Len()-start, err)
	return err
}

// readBuffer is bufio.NewExtendedReader(c.Conn).ReadBuffer without the adapter's
// allocation.
func (c *rrFirstByteConn) readBuffer(buffer *buf.Buffer) error {
	if reader, isExtended := c.Conn.(N.ExtendedReader); isExtended {
		return reader.ReadBuffer(buffer)
	}
	n, err := c.Conn.Read(buffer.FreeBytes())
	buffer.Truncate(n)
	if n > 0 && errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// writeBuffer is bufio.NewExtendedWriter(c.Conn).WriteBuffer without the adapter's
// allocation.
func (c *rrFirstByteConn) writeBuffer(buffer *buf.Buffer) error {
	if writer, isExtended := c.Conn.(N.ExtendedWriter); isExtended {
		return writer.WriteBuffer(buffer)
	}
	defer buffer.Release()
	_, err := c.Conn.Write(buffer.Bytes())
	return err
}

// Write passes the client's data to the node's connection; the first non-empty
// write marks the client as having sent something. An empty write before it is the
// nudge of kickWriteHandshake (route/conn.go): it reaches the node's connection
// only when that one has a write handshake of its own, as without the wrapper.
func (c *rrFirstByteConn) Write(p []byte) (int, error) {
	if c.state.Load()&rrStateWrote == 0 {
		if len(p) == 0 {
			if N.NeedHandshakeForWrite(c.Conn) {
				return c.Conn.Write(p)
			}
			return 0, nil
		}
		c.wrote()
	}
	return c.Conn.Write(p)
}

func (c *rrFirstByteConn) WriteBuffer(buffer *buf.Buffer) error {
	if c.state.Load()&rrStateWrote == 0 {
		if buffer.IsEmpty() && !N.NeedHandshakeForWrite(c.Conn) {
			buffer.Release()
			return nil
		}
		if !buffer.IsEmpty() {
			c.wrote()
		}
	}
	return c.writeBuffer(buffer)
}

// LxClientFinished is the client's end of stream (route/conn.go tells it when the
// wrapper lies on the write path): a close without a byte after it is no implicit
// REFUSE_DST; the connection still waits as any other.
func (c *rrFirstByteConn) LxClientFinished(bool) {
	c.set(rrStateHalfClosed)
}

// CloseWrite is the half-close from the client while writes still go through the
// wrapper. Without a half-close below it closes the connection, as the copy does
// for a writer that has none.
func (c *rrFirstByteConn) CloseWrite() error {
	c.set(rrStateHalfClosed)
	if closer, isCloser := N.UnwrapWriter(c.Conn).(N.WriteCloser); isCloser {
		return closer.CloseWrite()
	}
	return c.Close()
}

// Close from our side is CANCEL: nothing follows.
func (c *rrFirstByteConn) Close() error {
	c.set(rrStateClosed)
	c.untrack()
	err := c.Conn.Close()
	c.dial.release()
	return err
}

// NeedHandshakeForRead holds until the first byte: then the copy re-selects its path.
func (c *rrFirstByteConn) NeedHandshakeForRead() bool {
	return c.state.Load()&rrStateGotByte == 0 || N.NeedHandshakeForRead(c.Conn)
}

// NeedHandshakeForWrite holds until the client's first write, and while the node's
// connection has a write handshake of its own: then the upload copy re-selects its
// path.
func (c *rrFirstByteConn) NeedHandshakeForWrite() bool {
	return c.state.Load()&rrStateWrote == 0 || N.NeedHandshakeForWrite(c.Conn)
}

// CreateReadWaiter hands out the read waiter of the node's connection; before the
// first byte it peeks at each buffer.
func (c *rrFirstByteConn) CreateReadWaiter() (N.ReadWaiter, bool) {
	waiter, created := bufio.CreateReadWaiter(c.Conn)
	if !created {
		return nil, false
	}
	if c.state.Load()&rrStateGotByte != 0 {
		return waiter, true
	}
	return &rrFirstByteReadWaiter{ReadWaiter: waiter, conn: c}, true
}

func (c *rrFirstByteConn) ReaderReplaceable() bool {
	return c.state.Load()&rrStateGotByte != 0
}

func (c *rrFirstByteConn) WriterReplaceable() bool {
	return c.state.Load()&rrStateWrote != 0
}

func (c *rrFirstByteConn) Upstream() any {
	return c.Conn
}

type rrFirstByteReadWaiter struct {
	N.ReadWaiter
	conn *rrFirstByteConn
}

func (w *rrFirstByteReadWaiter) WaitReadBuffer() (*buf.Buffer, error) {
	buffer, err := w.ReadWaiter.WaitReadBuffer()
	if w.conn.state.Load()&rrStateGotByte == 0 {
		var n int
		if err == nil {
			n = buffer.Len()
		}
		w.conn.observe(n, err)
	}
	return buffer, err
}

// lx:end SPEC 138
