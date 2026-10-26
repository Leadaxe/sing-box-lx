package wireguard

// lx: SPEC 139 — connection-test trace helpers for DialContext and
// ListenPacketWithDestination. A domain handed to this endpoint is resolved
// by the DNS rules (`lookup` with resolver dns_rules), and the connection
// inside the tunnel is opened by netstack, not DefaultDialer, so the socket
// stage is recorded here. Without a trace in ctx each helper costs one
// ctx.Value lookup and records nothing.

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lookupLx is w.dnsRouter.Lookup inside a `lookup` stage with the
// resolve-cycle check. The stage ends before the dial; the caller dials in
// its own ctx.
func (w *Endpoint) lookupLx(ctx context.Context, domain string) ([]netip.Addr, error) {
	lookupCtx, stage, err := lxtrace.BeginLookup(ctx, "dns_rules", domain)
	if err != nil {
		return nil, err
	}
	addresses, err := w.dnsRouter.Lookup(lookupCtx, domain, adapter.DNSQueryOptions{})
	if stage != nil && len(addresses) > 0 {
		stage.Attr("addresses", strings.Join(common.Map(addresses, netip.Addr.String), ","))
	}
	stage.End(err)
	return addresses, err
}

// netstackLx returns the netstack dialer, wrapped to record one
// `tcp_connect` / `udp` stage per inner dial when ctx carries a trace.
func (w *Endpoint) netstackLx(ctx context.Context) N.Dialer {
	if lxtrace.From(ctx) == nil {
		return w.endpoint
	}
	return &netstackTraceDialerLx{Dialer: w.endpoint, inside: w.Tag()}
}

type netstackTraceDialerLx struct {
	N.Dialer
	inside string
}

func (d *netstackTraceDialerLx) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	kind := "tcp_connect"
	if N.NetworkName(network) == N.NetworkUDP {
		kind = "udp"
	}
	_, stage := lxtrace.Begin(ctx, kind, destination.String())
	stage.Attr("inside", d.inside)
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	stage.End(err)
	return conn, err
}

func (d *netstackTraceDialerLx) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	_, stage := lxtrace.Begin(ctx, "udp", destination.String())
	stage.Attr("inside", d.inside)
	conn, err := d.Dialer.ListenPacket(ctx, destination)
	stage.End(err)
	return conn, err
}
