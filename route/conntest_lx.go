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
	C "github.com/sagernet/sing-box/constant"
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

// beginDialLx opens the `dial` stage of the first hop: the node the route
// selected, dialing the final destination. The router resolves selectors
// itself (metadata.OutboundChain, upstream 03335338a), so `this` is already
// the node a selector points at (SPEC 139 §4, Appendix A: `group proxy →
// vless-nl`, then `dial vless-nl`); the groups passed on the way are the
// `via_group` attribute. A urltest/failover/round_robin group keeps its own
// tag (route/group_resolve_lx.go): its pick is per connection and is named by
// the `group` stage the group writes under this dial.
func beginDialLx(ctx context.Context, this N.Dialer, metadata *adapter.InboundContext) (context.Context, *lxtrace.Stage) {
	if lxtrace.From(ctx) == nil {
		return ctx, nil
	}
	var tag, outboundType string
	var viaGroups []string
	if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
		if len(metadata.OutboundChain) > 1 {
			for _, element := range metadata.OutboundChain[:len(metadata.OutboundChain)-1] {
				viaGroups = append(viaGroups, element.Tag())
			}
		} else {
			outbound, viaGroups = resolveSelectorsLx(ctx, outbound)
		}
		tag = outbound.Tag()
		outboundType = outbound.Type()
	}
	ctx, stage := lxtrace.Begin(ctx, "dial", tag)
	if outboundType != "" {
		stage.Attr("type", outboundType)
	}
	if len(viaGroups) > 0 {
		stage.Attr("via_group", strings.Join(viaGroups, ","))
	}
	stage.Attr("destination", metadata.Destination.String())
	if len(metadata.DestinationAddresses) > 0 {
		stage.Attr("addresses", strings.Join(common.Map(metadata.DestinationAddresses, netip.Addr.String), ","))
	}
	return ctx, stage
}

// resolveSelectorsLx follows nested selectors to the outbound they point at
// right now and returns the chain of group tags passed: the dial of a caller
// that did not route (no OutboundChain). Any other group, or a selector whose
// pick is unknown to the manager, is returned as is.
func resolveSelectorsLx(ctx context.Context, outbound adapter.Outbound) (adapter.Outbound, []string) {
	var via []string
	manager := lxtrace.OutboundsOf(ctx)
	seen := make(map[string]bool)
	for outbound.Type() == C.TypeSelector && manager != nil && !seen[outbound.Tag()] {
		group, isGroup := outbound.(adapter.OutboundGroup)
		if !isGroup {
			break
		}
		seen[outbound.Tag()] = true
		next, loaded := manager.Outbound(adapter.GroupNow(group))
		if !loaded {
			break
		}
		via = append(via, outbound.Tag())
		outbound = next
	}
	return outbound, via
}
