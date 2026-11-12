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

	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
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

// Dialer wraps the UDP conns a dialer hands out with TruncateReads and, where
// lx.quic_sniff_decoy covers outboundType, runs the SPEC 151 sniff decoy;
// TCP dials and ListenPacket pass through untouched by this package. It is the
// seam for clients that dial inside a library (sing-quic's hysteria2 and tuic).
func Dialer(ctx context.Context, outboundType string, outboundDialer N.Dialer, logger logger.Logger) N.Dialer {
	// lx: SPEC 152 — this is the dialer the QUIC client opens its session
	// underlay with; the session is internal to the tunnel, so a selector in
	// the detour drops it on every node switch (see dialer.NewSessionDialerLx).
	return &truncDialer{Dialer: dialer.NewSessionDialerLx(outboundDialer), logger: logger, decoy: NewSniffDecoy(ctx, outboundType, logger)}
}

// youngConnWindow is how long a conn handed to the QUIC library must live to
// count as a successful attempt (lx: SPEC 151). The library does not report
// its handshake to the dialer; a conn it closed sooner than this failed the
// handshake, the authentication or died right after, which through a
// sniffing relay is what happens. hysteria2 authenticates within 15 s by
// default, quic-go's handshake idle timeout is 5 s; a false verdict on a conn
// that died young for another reason costs one byte on the next dial.
const youngConnWindow = 30 * time.Second

type truncDialer struct {
	N.Dialer
	logger logger.Logger
	decoy  *Decoy
	access sync.Mutex
	last   *attemptConn
}

func (d *truncDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkUDP || !d.decoy.Enabled() {
		conn, err := d.Dialer.DialContext(ctx, network, destination)
		if err != nil {
			return nil, err
		}
		if N.NetworkName(network) != N.NetworkUDP {
			return conn, nil
		}
		return TruncateReads(conn, d.logger), nil
	}
	d.judgePrevious()
	attempt := d.decoy.Begin(ctx)
	conn, err := d.Dialer.DialContext(attempt.Context, network, destination)
	if err != nil {
		return nil, err
	}
	attempt.Dialed(conn, conn)
	wrapped := &attemptConn{Conn: TruncateReads(conn, d.logger), attempt: attempt, dialedAt: time.Now()}
	d.access.Lock()
	d.last = wrapped
	d.access.Unlock()
	return wrapped, nil
}

// judgePrevious reports the previous attempt to the memory at the moment it
// matters — the next dial: still open or lived past youngConnWindow is a
// success, closed sooner is a failure. The redo verdict is ignored, the
// library is already dialling again.
func (d *truncDialer) judgePrevious() {
	d.access.Lock()
	last := d.last
	d.last = nil
	d.access.Unlock()
	if last == nil {
		return
	}
	var err error
	if closedAt := last.closedAt.Load(); closedAt != 0 && time.Duration(closedAt-last.dialedAt.UnixNano()) < youngConnWindow {
		err = errYoungConn
	}
	last.attempt.Done(err)
}

var errYoungConn = E.New("conn closed before the handshake could have settled")

// attemptConn is the conn handed to the QUIC library, remembering when it was
// dialled and closed so the dialer can judge the attempt later.
type attemptConn struct {
	net.Conn
	attempt  *Attempt
	dialedAt time.Time
	closedAt atomic.Int64
}

func (c *attemptConn) Close() error {
	c.closedAt.CompareAndSwap(0, time.Now().UnixNano())
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
