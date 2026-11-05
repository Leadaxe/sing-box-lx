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

	"github.com/sagernet/sing-box/common/dialer"
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

// Dialer wraps the UDP conns a dialer hands out with TruncateReads and, where
// lx.quic_sniff_decoy in ctx covers the conn, sends the SPEC 151 sniff decoy;
// TCP dials and ListenPacket pass through untouched by this package. It is the
// seam for clients that dial inside a library (sing-quic's hysteria2 and tuic).
func Dialer(ctx context.Context, outboundDialer N.Dialer, logger logger.Logger) N.Dialer {
	// lx: SPEC 152 — this is the dialer the QUIC client opens its session
	// underlay with; the session is internal to the tunnel, so a selector in
	// the detour drops it on every node switch (see dialer.NewSessionDialerLx).
	return &truncDialer{Dialer: dialer.NewSessionDialerLx(outboundDialer), logger: logger, sniffDecoy: SniffDecoyFromContext(ctx)}
}

type truncDialer struct {
	N.Dialer
	logger     logger.Logger
	sniffDecoy string
}

func (d *truncDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	if N.NetworkName(network) != N.NetworkUDP {
		return conn, nil
	}
	if SniffDecoyApplies(d.sniffDecoy, conn) {
		WriteSniffDecoy(conn, d.logger)
	}
	return TruncateReads(conn, d.logger), nil
}

func (d *truncDialer) Upstream() any {
	return d.Dialer
}
