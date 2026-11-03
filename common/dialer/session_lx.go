package dialer

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 152 — the dialer a tunnel opens its session underlay with.
//
// A tunnel outbound (masque, trusttunnel, hysteria2, tuic) builds one session
// in the context of the first user connection that needs it and serves every
// later connection over it. The router marks that context as an external
// connection, so a selector in the tunnel's `detour` registered the underlay
// as external and left it alone on a node switch unless
// `interrupt_exist_connections` was set: the tunnel kept running through the
// old node for as long as the session lived. Upstream fixes the same thing for
// its multiplexed clients (mux, anytls, ssh, snell, gRPC/HTTP transports) with
// ContextForMultiplexSession; the WireGuard bind never had the problem, its
// socket is dialled from the endpoint's own context. This wrapper applies the
// same contract to the session underlay: the dial is internal to the tunnel,
// not tied to the connection that happened to trigger it.
func NewSessionDialerLx(dialer N.Dialer) N.Dialer {
	return &sessionDialer{Dialer: dialer}
}

type sessionDialer struct {
	N.Dialer
}

func (d *sessionDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d.Dialer.DialContext(adapter.ContextForMultiplexSession(ctx), network, destination)
}

func (d *sessionDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return d.Dialer.ListenPacket(adapter.ContextForMultiplexSession(ctx), destination)
}

func (d *sessionDialer) Upstream() any {
	return d.Dialer
}
