package http

// lx: SPEC 110 — what the Cloudflare WARP dialect of CONNECT-IP needs from the
// shared HTTP client, kept out of the upstream files as far as possible.

import (
	"context"
	"errors"
	"net/http"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// TunnelLXOptions are set by the masque-client endpoint from its `profile`.
type TunnelLXOptions struct {
	// PlainConnectHTTP2 sends the tunnel request on HTTP/2 as an ordinary
	// CONNECT (:method and :authority only) instead of an Extended CONNECT with
	// :protocol/:scheme/:path. WARP never announces
	// SETTINGS_ENABLE_CONNECT_PROTOCOL and answers PROTOCOL_ERROR to the
	// extended form; the tunnel type travels in PlainConnectHeaders instead.
	PlainConnectHTTP2 bool
	// PlainConnectHeaders are added to the HTTP/2 tunnel request only (WARP:
	// cf-connect-proto, pq-enabled). Values already present win.
	PlainConnectHeaders http.Header
	// HTTP3ConnectBudget bounds the wait for the CONNECT response on HTTP/3.
	// An endpoint that completes the QUIC handshake and then stays silent (WARP
	// behind an address it dislikes, a hop that drops the stream) is otherwise
	// retried on HTTP/3 forever: only a failed handshake counts as
	// ErrHTTP3Unavailable upstream. Zero keeps upstream's behaviour.
	HTTP3ConnectBudget time.Duration
	// HTTP3AdditionalSettings are extra SETTINGS for the HTTP/3 connection
	// (WARP's official client still advertises the legacy
	// SETTINGS_H3_DATAGRAM_00, 0x276).
	HTTP3AdditionalSettings map[uint64]uint64
}

// openTunnelHTTP3 is the HTTP/3 leg of openTunnel. With a connect budget a
// silent CONNECT is reported as ErrHTTP3Unavailable so the caller falls back
// and marks HTTP/3 broken the way it does for a failed handshake.
func (c *Client) openTunnelHTTP3(ctx context.Context, request tunnelRequest) (DatagramStream, error) {
	budget := c.tunnelLX.HTTP3ConnectBudget
	if budget <= 0 {
		return c.http3.OpenTunnel(ctx, request)
	}
	budgetCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	stream, err := c.http3.OpenTunnel(budgetCtx, request)
	if err != nil && ctx.Err() == nil && errors.Is(budgetCtx.Err(), context.DeadlineExceeded) {
		return nil, E.Cause1(ErrHTTP3Unavailable, E.New("no CONNECT response within ", budget))
	}
	return stream, err
}

// tunnelHeadersHTTP2 writes the tunnel-type part of the HTTP/2 request header:
// :protocol for an Extended CONNECT, the profile's plain headers otherwise.
func (c *Client) tunnelHeadersHTTP2(header http.Header, protocol string) {
	if !c.tunnelLX.PlainConnectHTTP2 {
		header.Set(":protocol", protocol)
		return
	}
	for name, values := range c.tunnelLX.PlainConnectHeaders {
		if header.Get(name) != "" {
			continue
		}
		for _, value := range values {
			header.Add(name, value)
		}
	}
}
