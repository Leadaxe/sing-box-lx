package transport

// lx: SPEC 139 — a DoH query of a connection test with fresh=true.
//
// DoH keeps its connections in the HTTP client of the transport, not in the
// DNS connection pool, so a fresh query (§5.6) would ride a live production
// connection. Instead it goes through a one-off client built like the
// production one and closed as soon as the exchange is done; the production
// client is not touched — no idle close, no reset, and a fresh query's
// timeout does not reset it (the guard in Exchange). The `dns` stage gets
// connection=new and pool=detached once the one-off client opened a
// connection, as a fresh acquire of the pool marks it.
//
// Without a trace in ctx the helper costs one ctx.Value lookup.

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing-box/common/lxtrace"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// freshTransportLx returns the client the exchange runs on and the release
// to call once the response is consumed: current itself and a no-op, or for
// a fresh query a one-off client that release closes.
func (t *HTTPSTransport) freshTransportLx(ctx context.Context, current *HTTPSTransportWrapper) (*HTTPSTransportWrapper, func()) {
	if !isFreshLx(ctx) {
		return current, releaseNothingLx
	}
	freshDialer := &freshDialerLx{Dialer: t.dialer, stage: lxtrace.Current(ctx)}
	freshTransport := NewHTTPSTransportWrapper(freshDialer, t.serverAddr, t.fallback)
	return freshTransport, freshTransport.Close
}

func releaseNothingLx() {}

// freshDialerLx marks the `dns` stage when the one-off client opens its
// first connection.
type freshDialerLx struct {
	N.Dialer
	stage  *lxtrace.Stage
	marked atomic.Bool
}

func (d *freshDialerLx) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err == nil {
		MarkFreshConnectionLx(d.stage, &d.marked)
	}
	return conn, err
}

// MarkFreshConnectionLx records on stage, once per marked, that the query ran
// on a connection of its own outside any pool. Shared with DoH3.
func MarkFreshConnectionLx(stage *lxtrace.Stage, marked *atomic.Bool) {
	if stage == nil || !marked.CompareAndSwap(false, true) {
		return
	}
	stage.Attr("connection", "new")
	stage.Attr("pool", "detached")
}
