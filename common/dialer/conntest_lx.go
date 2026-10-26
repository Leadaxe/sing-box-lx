package dialer

// lx: SPEC 139 — connection-test trace helpers for detour.go, resolve.go and
// default.go. Without a trace in ctx every helper costs one ctx.Value lookup
// and records nothing.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// beginDetourLx opens the `detour` stage: the connection is handed to the
// detour outbound with a domain or an address.
func (d *DetourDialer) beginDetourLx(ctx context.Context, dialer N.Dialer, destination M.Socksaddr) (context.Context, *lxtrace.Stage) {
	if lxtrace.From(ctx) == nil {
		return ctx, nil
	}
	tag := d.detour
	if outbound, isOutbound := dialer.(adapter.Outbound); isOutbound {
		tag = outbound.Tag()
	}
	ctx, stage := lxtrace.Begin(ctx, "detour", tag)
	stage.Attr("destination", destination.String())
	if destination.IsDomain() {
		stage.Attr("passed", "domain")
	} else {
		stage.Attr("passed", "address")
	}
	return ctx, stage
}

// resolverNameLx names who resolves the server address of a node.
func (d *resolveDialer) resolverNameLx() string {
	if d.queryOptions.Transport != nil {
		return "domain_resolver:" + d.queryOptions.Transport.Tag()
	}
	return "default_domain_resolver"
}

// lookupLx is d.router.Lookup inside a `lookup` stage with the resolve-cycle
// check. The stage ends before the caller dials, so the dial stages are its
// siblings, not its children: the caller keeps dialing in its own ctx.
func (d *resolveDialer) lookupLx(ctx context.Context, domain string) ([]netip.Addr, error) {
	lookupCtx, stage, err := lxtrace.BeginLookup(ctx, d.resolverNameLx(), domain)
	if err != nil {
		return nil, err
	}
	addresses, err := d.router.Lookup(lookupCtx, domain, d.queryOptions)
	if stage != nil && len(addresses) > 0 {
		stage.Attr("addresses", strings.Join(common.Map(addresses, netip.Addr.String), ","))
	}
	stage.End(err)
	return addresses, err
}

// beginConnectLx opens the `tcp_connect` / `udp` stage of one socket.
func beginConnectLx(ctx context.Context, network string, address M.Socksaddr) *lxtrace.Stage {
	if lxtrace.From(ctx) == nil {
		return nil
	}
	kind := "tcp_connect"
	if N.NetworkName(network) == N.NetworkUDP {
		kind = "udp"
	}
	_, stage := lxtrace.Begin(ctx, kind, address.String())
	return stage
}

// endConnLx / endPacketConnLx close a socket stage. A dial cancelled while
// ctx is cancelled is a loser of a parallel dial (N.DialParallel,
// DialParallelNetwork and the interface race cancel the losers once a
// sibling wins): it ends Skipped with lost_race, never as the root cause
// (SPEC 139 §5.13). The winner is not known here, so lost_race carries 0.
func endConnLx(ctx context.Context, stage *lxtrace.Stage, conn net.Conn, err error) {
	if stage == nil {
		return
	}
	if err == nil && conn != nil {
		if slowConn, isSlow := conn.(*slowOpenConn); isSlow && slowConn.conn.Load() == nil {
			// TCP Fast Open: the connect happens with the first write.
			stage.Attr("tfo", "deferred")
		} else {
			stage.Attr("local", conn.LocalAddr().String())
		}
	}
	endSocketLx(ctx, stage, err)
}

func endPacketConnLx(ctx context.Context, stage *lxtrace.Stage, conn net.PacketConn, err error) {
	if stage == nil {
		return
	}
	if err == nil && conn != nil {
		stage.Attr("local", conn.LocalAddr().String())
	}
	endSocketLx(ctx, stage, err)
}

func endSocketLx(ctx context.Context, stage *lxtrace.Stage, err error) {
	if err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil {
		stage.LostRace(nil)
		return
	}
	stage.End(err)
}
