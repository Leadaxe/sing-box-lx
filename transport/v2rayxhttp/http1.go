package v2rayxhttp

import (
	"context"
	stdTLS "crypto/tls"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// http1IdleConnTimeout matches Xray's HTTP/1.1 client (IdleConnTimeout 300 s).
const http1IdleConnTimeout = 300 * time.Second

// lx: SPEC 104 — http1XmuxConn is one pooled HTTP/1.1 client. Unlike HTTP/2 it
// is a logical resource: long requests (download GET, streamed bodies) each take
// their own TCP connection with "Connection: close", as Xray's DisableKeepAlives
// client does, while packet-up upload POSTs reuse keep-alive connections. The
// xmux limits count against the client, as in Xray.
type http1XmuxConn struct {
	transport *http.Transport
	closed    atomic.Bool
}

// newHTTP1Transport builds an HTTP/1.1-only transport. tlsDialer is nil for a
// cleartext server.
func newHTTP1Transport(dialer N.Dialer, tlsDialer tls.Dialer) *http.Transport {
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		// A non-nil empty map turns HTTP/2 off even if the server offers it.
		TLSNextProto:    map[string]func(string, *stdTLS.Conn) http.RoundTripper{},
		IdleConnTimeout: http1IdleConnTimeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
		},
	}
	if tlsDialer != nil {
		transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
		}
	}
	return transport
}

func (c *http1XmuxConn) Close() {
	if c.closed.Swap(true) {
		return
	}
	c.transport.CloseIdleConnections()
}

func (c *http1XmuxConn) IsClosed() bool {
	return c.closed.Load()
}

func (c *http1XmuxConn) roundTripper() http.RoundTripper {
	return c.transport
}
