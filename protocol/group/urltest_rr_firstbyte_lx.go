package group

// lx:begin SPEC 138 round_robin first byte (stage 2)
//
// A connection of a round_robin group succeeds with the first byte that comes from
// the destination; an opened connection is not a success yet. It fails when it ends
// with no byte received although the client sent something (SPEC 138 §3.1.2):
// VLESS, VMess, Trojan, Shadowsocks, a live mux session, HTTP/2 transports,
// hysteria2 and TUIC hand out a connection before the destination answers, so a
// dead destination shows only that way. No timer measures "no byte": a connection
// may be silent legitimately. A close or a half-close from our side before the
// first byte is not a failure.
//
// The wrapper sits between the node's connection and the group's interrupt wrapper
// and costs the connection no fast read path:
//   - before the first byte it hands the copy the read waiter of the node's
//     connection, peeking at each buffer (CreateReadWaiter), and declares
//     NeedHandshakeForRead; with the first byte the copy's transfer hook sees the
//     handshake completed (N.HandshakeState.Check), re-selects its path, and from
//     then on gets the bare read waiter (or splice/vectorised reads: the wrapper
//     becomes ReaderReplaceable);
//   - writes always go through the wrapper (WriterReplaceable is false), so a
//     half-close from the client reaches it. It does not declare
//     NeedHandshakeForWrite: kickWriteHandshake (route/conn.go) finds the write
//     handshake of the node's connection through Upstream, as without the wrapper.
//
// Not wrapped: a connection test (SPEC 139), a pick outside the pool, a nested
// urltest (it watches its own connections), and a direct leaf — an unreachable
// destination shows on its open, and the wrapper would cost it splice. Its open
// counts as the first byte.

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

// rrClockBase is the origin of the monotonic marks of the self-heal (rrClock).
var rrClockBase = time.Now()

// rrClock returns the monotonic time since rrClockBase; never 0, which means "never".
func rrClock(at time.Time) int64 {
	return int64(at.Sub(rrClockBase)) + 1
}

// rrConnWatch is what a watched connection reports to its group.
type rrConnWatch struct {
	s      *URLTest
	member string    // the pool member the connection went through
	start  time.Time // the start of the connection (its pick)
	key    rrKey     // the shift-table key; invalid without stickiness
	shift  int       // the shift of the key at the pick
	// confirmFor is the member whose failed dial this retry replaced: the first
	// byte of the retry confirms that failure (§3.6.2, case 1). failure is that
	// failed dial, so the key shift and the confirmation do not count it twice.
	confirmFor string
	failure    *rrFailure
}

// rrWatchConn wraps an opened TCP connection of a round_robin group to watch for
// its first byte (DialContext and the retry). It returns conn as is when there is
// nothing to watch; for a direct leaf the open itself is the first byte.
func (s *URLTest) rrWatchConn(ctx context.Context, network string, dialed adapter.Outbound, conn net.Conn, picked rrPick) net.Conn {
	if s.balancer == nil {
		return conn
	}
	return s.rrWatch(ctx, network, dialed, conn, picked, "", nil)
}

func (s *URLTest) rrWatch(ctx context.Context, network string, dialed adapter.Outbound, conn net.Conn, picked rrPick, confirmFor string, failure *rrFailure) net.Conn {
	if N.NetworkName(network) != N.NetworkTCP || lxtrace.IsProbe(ctx) || s.group.ctx.Err() != nil {
		return conn
	}
	member := dialed.Tag() // a chain hop carries the tag of its member
	if !s.balancer.inPool(member) {
		return conn // a fallback pick outside the pool (cold start)
	}
	node, loaded := s.outbound.Outbound(member)
	if !loaded || rrNestedURLTest(node) {
		return conn
	}
	s.balancer.rrCountConn(member, &s.balancer.heal)
	watch := &rrConnWatch{
		s:          s,
		member:     member,
		start:      picked.start,
		key:        picked.key,
		shift:      picked.shift,
		confirmFor: confirmFor,
		failure:    failure,
	}
	if rrDirectLeaf(dialed) {
		s.rrFirstByte(watch)
		return conn
	}
	return newRRFirstByteConn(conn, watch)
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
	rrStateWrote   uint32 = 1 << iota // the client sent a byte
	rrStateGotByte                    // the destination sent a byte
	rrStateClosed                     // closed or half-closed from our side
	rrStateDone                       // the outcome is reported
)

// rrFirstByteConn watches a connection for its first byte (see the file header).
type rrFirstByteConn struct {
	net.Conn
	reader N.ExtendedReader
	writer N.ExtendedWriter
	state  atomic.Uint32
	watch  *rrConnWatch
}

func newRRFirstByteConn(conn net.Conn, watch *rrConnWatch) *rrFirstByteConn {
	return &rrFirstByteConn{
		Conn:   conn,
		reader: bufio.NewExtendedReader(conn),
		writer: bufio.NewExtendedWriter(conn),
		watch:  watch,
	}
}

func (c *rrFirstByteConn) set(bit uint32) uint32 {
	for {
		old := c.state.Load()
		if old&bit == bit || c.state.CompareAndSwap(old, old|bit) {
			return old
		}
	}
}

// observe takes the outcome of one read before the first byte.
func (c *rrFirstByteConn) observe(n int, err error) {
	if n > 0 {
		if old := c.set(rrStateGotByte | rrStateDone); old&rrStateDone == 0 {
			c.watch.s.rrFirstByte(c.watch)
		}
		return
	}
	if err == nil || E.IsTimeout(err) {
		return // a read deadline of the consumer is no outcome: still watched
	}
	old := c.set(rrStateDone)
	if old&(rrStateDone|rrStateGotByte|rrStateClosed) != 0 || old&rrStateWrote == 0 {
		return // reported already, closed by us, or the client sent nothing
	}
	c.watch.s.rrSilentClose(c.watch)
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
		return c.reader.ReadBuffer(buffer)
	}
	start := buffer.Len()
	err := c.reader.ReadBuffer(buffer)
	c.observe(buffer.Len()-start, err)
	return err
}

func (c *rrFirstByteConn) Write(p []byte) (int, error) {
	if len(p) > 0 && c.state.Load()&rrStateWrote == 0 {
		c.set(rrStateWrote)
	}
	return c.Conn.Write(p)
}

func (c *rrFirstByteConn) WriteBuffer(buffer *buf.Buffer) error {
	if !buffer.IsEmpty() && c.state.Load()&rrStateWrote == 0 {
		c.set(rrStateWrote)
	}
	return c.writer.WriteBuffer(buffer)
}

// CloseWrite is the half-close from the client: the connection may end without a
// byte now, and that is not a failure. Without a half-close below it closes the
// connection, as the copy does for a writer that has none.
func (c *rrFirstByteConn) CloseWrite() error {
	c.set(rrStateClosed)
	if closer, isCloser := N.UnwrapWriter(c.Conn).(N.WriteCloser); isCloser {
		return closer.CloseWrite()
	}
	return c.Conn.Close()
}

// Close from our side before the first byte, after the client sent something, is a
// client that gave up: after a long wait a reason for a check (rrHang).
func (c *rrFirstByteConn) Close() error {
	old := c.set(rrStateClosed)
	if old&(rrStateClosed|rrStateDone|rrStateGotByte) == 0 && old&rrStateWrote != 0 {
		c.watch.s.rrHang(c.watch.member, c.watch.start)
	}
	return c.Conn.Close()
}

// NeedHandshakeForRead holds until the first byte: then the copy re-selects its path.
func (c *rrFirstByteConn) NeedHandshakeForRead() bool {
	return c.state.Load()&rrStateGotByte == 0 || N.NeedHandshakeForRead(c.Conn)
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
	return false
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
