package v2rayxhttp

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
)

// lx: SPEC 142 — the HTTP/2 transport as Xray builds it
// (transport/internet/splithttp/dialer.go, httpVersion == "2").
const (
	// http2IdleConnTimeout is Xray's net.ConnIdleTimeout. Without it an
	// http2.ClientConn that was not idle when the pooled transport was retired
	// (a request still pending on it) is never closed: CloseIdleConnections
	// skips it and nothing else references the transport.
	http2IdleConnTimeout = 300 * time.Second
	// http2DefaultKeepAlivePeriod is Xray's net.ChromeH2KeepAlivePeriod — the h2
	// health-check ping period when h_keep_alive_period is unset.
	http2DefaultKeepAlivePeriod = 45 * time.Second
)

// http2ReadIdleTimeout maps xmux.h_keep_alive_period to http2.Transport: 0 is
// Xray's 45 s default (x/net's own 0 means no pings at all), a negative value
// turns the pings off. Mirrors http3KeepAlivePeriod.
func http2ReadIdleTimeout(period time.Duration) time.Duration {
	switch {
	case period == 0:
		return http2DefaultKeepAlivePeriod
	case period < 0:
		return 0
	default:
		return period
	}
}

// newHTTP2Transport builds one pooled HTTP/2 transport over tlsDialer.
func newHTTP2Transport(tlsDialer tls.Dialer, keepAlivePeriod time.Duration) *http2.Transport {
	return &http2.Transport{
		IdleConnTimeout: http2IdleConnTimeout,
		ReadIdleTimeout: http2ReadIdleTimeout(keepAlivePeriod),
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
			return tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
		},
	}
}
