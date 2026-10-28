package quic

// lx: SPEC 139 — a DoH3 query of a connection test with fresh=true.
//
// DoH3 keeps its QUIC connections in the http3 client of the transport, not
// in the DNS connection pool, so a fresh query (§5.6) would ride a live
// production connection. Instead it goes through a one-off client built by
// newTransport and closed as soon as the exchange is done; the production
// client is not touched. The `dns` stage gets connection=new and
// pool=detached once the one-off client opened a connection (as for DoH,
// dns/transport/https_fresh_lx.go). DoQ needs nothing here: it takes its
// connection from the DNS connection pool, which handles fresh itself.
//
// Without a trace in ctx the helper costs one ctx.Value lookup.

import (
	"context"
	"sync/atomic"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/dns/transport"
)

// freshTransportLx returns the client the exchange runs on and the release
// to call once the response is consumed: current itself and a no-op, or for
// a fresh query a one-off client that release closes.
func (t *HTTP3Transport) freshTransportLx(ctx context.Context, current *http3.Transport) (*http3.Transport, func()) {
	if !lxtrace.Fresh(ctx) {
		return current, releaseNothingLx
	}
	freshTransport := t.newTransport()
	stage := lxtrace.Current(ctx)
	var marked atomic.Bool
	dial := freshTransport.Dial
	freshTransport.Dial = func(ctx context.Context, addr string, tlsConfig *tls.STDConfig, config *quic.Config) (*quic.Conn, error) {
		conn, err := dial(ctx, addr, tlsConfig, config)
		if err == nil {
			transport.MarkFreshConnectionLx(stage, &marked)
		}
		return conn, err
	}
	return freshTransport, func() {
		freshTransport.Close()
	}
}

func releaseNothingLx() {}
