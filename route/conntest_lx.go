package route

// lx: SPEC 139 — connection-test trace helpers for route/route.go and
// route/conn.go. Without a trace in ctx every helper costs one ctx.Value
// lookup and records nothing.

import (
	"context"
	"io"
	"net/netip"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

// closeRoutedLx ends a connection that a DEPTH_ROUTE / DEPTH_DNS test stops
// right after the route decision: no tracker, no dial. onClose is called so
// a RouteConnection caller waiting for it returns.
func closeRoutedLx(conn io.Closer, onClose N.CloseHandlerFunc) {
	conn.Close()
	if onClose != nil {
		onClose(nil)
	}
}

// beginDialLx opens the `dial` stage of the first hop: the outbound the
// route selected, dialing the final destination.
func beginDialLx(ctx context.Context, this N.Dialer, metadata *adapter.InboundContext) (context.Context, *lxtrace.Stage) {
	if lxtrace.From(ctx) == nil {
		return ctx, nil
	}
	var tag, outboundType string
	if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
		tag = outbound.Tag()
		outboundType = outbound.Type()
	}
	ctx, stage := lxtrace.Begin(ctx, "dial", tag)
	if outboundType != "" {
		stage.Attr("type", outboundType)
	}
	stage.Attr("destination", metadata.Destination.String())
	if len(metadata.DestinationAddresses) > 0 {
		stage.Attr("addresses", strings.Join(common.Map(metadata.DestinationAddresses, netip.Addr.String), ","))
	}
	return ctx, stage
}
