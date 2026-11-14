package lxdatagram

// lx: SPEC 150 — kernel semantics for a datagram conn carried in a stream.
//
// A kernel UDP socket silently truncates a datagram that does not fit the
// caller's buffer; quic-go is written against that contract (an oversize
// datagram ends up undecryptable and is dropped, its listen loop only skips
// EMSGSIZE and closes the transport on any other read error). A UDP conn
// tunnelled through a stream detour (vless XUDP or plain vless UDP over
// WebSocket, gRPC, XHTTP, …) delivers the datagram whole with a length prefix,
// and sing's packet readers answer a short buffer with io.ErrShortBuffer
// instead — fatal for quic-go. Cloudflare WARP sends PMTUD probes of 1453–1462
// bytes over the 1452 quic-go advertises a few seconds into every h3 session
// (stand 2026-11-01), so every MASQUE h3 tunnel through such a detour died
// there; upstream hysteria2 and tuic hand quic-go their detour conn through
// the same read path.

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// maxDatagramSize is the largest UDP payload a datagram can carry; a read
// buffer of this size never truncates, so such reads go to the conn directly.
const maxDatagramSize = 65535

// TruncateReads returns conn with kernel read semantics: a datagram longer
// than the caller's buffer is cut to the buffer and the rest is discarded,
// never carried into the next Read. A conn backed by a kernel socket
// (syscall.Conn once the replaceable readers are unwrapped — the direct path)
// already behaves that way and is returned unchanged.
func TruncateReads(conn net.Conn, logger logger.Logger) net.Conn {
	if IsKernelSocket(conn) {
		return conn
	}
	return &truncConn{Conn: conn, logger: logger}
}

type truncConn struct {
	net.Conn
	logger   logger.Logger
	access   sync.Mutex
	buffer   []byte
	reported bool
}

func (c *truncConn) Read(p []byte) (int, error) {
	if len(p) >= maxDatagramSize {
		return c.Conn.Read(p)
	}
	c.access.Lock()
	defer c.access.Unlock()
	if c.buffer == nil {
		c.buffer = make([]byte, maxDatagramSize)
	}
	n, err := c.Conn.Read(c.buffer)
	if n > len(p) {
		if !c.reported && c.logger != nil {
			c.reported = true
			c.logger.Debug("datagram of ", n, " bytes truncated to the ", len(p), "-byte read buffer (kernel semantics for a stream-carried conn)")
		}
		n = len(p)
	}
	copy(p, c.buffer[:n])
	return n, err
}

func (c *truncConn) Upstream() any {
	return c.Conn
}

// ReaderReplaceable is false: the wrapper must stay in the read path.
func (c *truncConn) ReaderReplaceable() bool {
	return false
}

func (c *truncConn) WriterReplaceable() bool {
	return true
}

var (
	_ common.WithUpstream  = (*truncConn)(nil)
	_ N.ReaderWithUpstream = (*truncConn)(nil)
	_ N.WriterWithUpstream = (*truncConn)(nil)
)

// Dialer wraps the UDP conns a dialer hands out with TruncateReads and, when
// lx.sniff_guard covers the node tagged tag, applies the guard to the QUIC
// handshake of every new connection (lx: SPEC 060). TCP dials and ListenPacket
// pass through untouched by this package. It is the seam for clients that
// dial inside a library (sing-quic's hysteria2 and tuic).
func Dialer(ctx context.Context, tag string, outboundDialer N.Dialer, logger logger.Logger) N.Dialer {
	// lx: SPEC 152 — this is the dialer the QUIC client opens its session
	// underlay with; the session is internal to the tunnel, so a selector in
	// the detour drops it on every node switch (see dialer.NewSessionDialerLx).
	return &truncDialer{
		Dialer: dialer.NewSessionDialerLx(outboundDialer),
		logger: logger,
		ctx:    ctx,
		node:   lxsniffguard.FromContext(ctx).Node(tag),
	}
}

// youngConnWindow is how long a conn handed to the QUIC library must live to
// count as a successful attempt (lx: SPEC 060). The library does not report
// its handshake to the dialer; a conn it closed sooner than this failed the
// handshake, the authentication or died right after, which through a
// sniffing relay is what happens. hysteria2 authenticates within 15 s by
// default, quic-go's handshake idle timeout is 5 s. A var for tests.
var youngConnWindow = 30 * time.Second

// afterFunc arms the youngConnWindow timer of a conn and returns its stop
// function; replaced in tests to fire the timer by hand.
var afterFunc = func(d time.Duration, f func()) (stop func() bool) {
	return time.AfterFunc(d, f).Stop
}

// truncDialer is handed to the QUIC library as its dialer. sing-quic dials a
// new connection with a context derived from the client's lifetime context
// (ClientOptions.Context, the outbound's build context), never with the
// context of the dial that asked for it, so the Attempt of the outbound
// wrapper does not reach here: the seam opens its own attempt on the node
// (lxsniffguard.Node.Begin) and reports the outcome later through the store.
//
// The dialer has no Close of its own: the outbound's Close reaches it through
// the conns. Outbound.Close calls the client's CloseWithError, which closes
// the QUIC transport and its raw conn (tuic: the attemptConn itself;
// hysteria2: the attemptConn, or the port-hop conn over it, which closes the
// current socket); attemptConn.Close stops the timer. A dial in flight is
// cancelled with the client's pending offer and returns no conn.
type truncDialer struct {
	N.Dialer
	logger logger.Logger
	// ctx is the build context of the outbound, which is also the client's
	// lifetime context: hysteria2 dials its port hops with it as is.
	ctx    context.Context
	node   *lxsniffguard.Node
	access sync.Mutex
	last   *attemptConn
}

func (d *truncDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkUDP {
		return d.Dialer.DialContext(ctx, network, destination)
	}
	if d.node == nil || sameContext(ctx, d.ctx) {
		conn, err := d.Dialer.DialContext(ctx, network, destination)
		if err != nil {
			return nil, err
		}
		if d.node != nil {
			// A port hop of hysteria2: the same QUIC connection moves to a
			// new socket, there is no handshake to guard. The connection
			// lived to the hop, which is the outcome of its attempt.
			d.hopped()
		}
		return TruncateReads(conn, d.logger), nil
	}
	d.judgePrevious()
	ctx, record := adapter.ContextWithCarrierRecord(ctx)
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	writeConn, handshake := GuardQUIC(d.node.Begin(), conn, record, TruncateReads(conn, d.logger), d.logger)
	wrapped := &attemptConn{Conn: writeConn, handshake: handshake, dialedAt: time.Now()}
	if handshake != nil {
		wrapped.stopTimer = afterFunc(youngConnWindow, wrapped.lived)
	}
	d.access.Lock()
	d.last = wrapped
	d.access.Unlock()
	return wrapped, nil
}

// sameContext compares two contexts without the run-time panic of an
// interface comparison on an incomparable dynamic type.
func sameContext(a, b context.Context) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

func (d *truncDialer) hopped() {
	d.access.Lock()
	last := d.last
	d.access.Unlock()
	if last != nil {
		last.hopped.Store(true)
		last.report(true)
	}
}

// judgePrevious reports the previous attempt at the next new connection when
// neither its timer nor a port hop did: closed within youngConnWindow is a
// failure, still open (or closed later, racing its timer) is a success.
func (d *truncDialer) judgePrevious() {
	d.access.Lock()
	last := d.last
	d.last = nil
	d.access.Unlock()
	if last == nil {
		return
	}
	closedAt := last.closedAt.Load()
	young := closedAt != 0 && time.Duration(closedAt-last.dialedAt.UnixNano()) < youngConnWindow && !last.hopped.Load()
	last.report(!young)
}

// attemptConn is the conn handed to the QUIC library, remembering when it was
// dialled and closed so its attempt can be judged: a success as soon as it
// has lived youngConnWindow or moved to a port hop, a failure at the next
// new connection when it was closed sooner. The attempt is reported once.
type attemptConn struct {
	net.Conn
	handshake *QUICHandshake
	dialedAt  time.Time
	closedAt  atomic.Int64
	hopped    atomic.Bool
	reported  atomic.Bool
	stopTimer func() bool
}

// lived runs when the conn reaches youngConnWindow.
func (c *attemptConn) lived() {
	if c.closedAt.Load() != 0 {
		// Closed while the timer fired: the next dial judges it by time.
		return
	}
	c.settle(true)
}

// report settles the attempt from outside the timer and stops it. stopTimer
// is read here only after d.access ordered it behind DialContext.
func (c *attemptConn) report(ok bool) {
	if c.stopTimer != nil {
		c.stopTimer()
	}
	c.settle(ok)
}

func (c *attemptConn) settle(ok bool) {
	if c.reported.CompareAndSwap(false, true) {
		c.handshake.Report(ok)
	}
}

func (c *attemptConn) Close() error {
	if c.closedAt.CompareAndSwap(0, time.Now().UnixNano()) && c.stopTimer != nil {
		c.stopTimer()
	}
	return c.Conn.Close()
}

func (c *attemptConn) Upstream() any {
	return c.Conn
}

func (c *attemptConn) ReaderReplaceable() bool {
	return true
}

func (c *attemptConn) WriterReplaceable() bool {
	return true
}

func (d *truncDialer) Upstream() any {
	return d.Dialer
}
