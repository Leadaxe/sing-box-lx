package group

// lx:begin SPEC 054 / SPEC 138 does a node go to the destination itself on open

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// siteOnOpenLx reports whether the leaf of node goes to the destination itself
// when a connection opens: a direct outbound, an endpoint (WireGuard, AWG,
// Tailscale, OpenVPN, OpenConnect) or an outbound that says so
// (adapter.DestinationDialerLx: MASQUE). Its refused or reset open is the site's
// answer. A proxy leaf fails its open between us and its server. node may be a
// group (its live leaf counts) or a chain hop (SPEC 073: it carries the tag of the
// original). Shared by SPEC 054 (penalties) and SPEC 138 (round_robin self-heal).
func siteOnOpenLx(ctx context.Context, outbounds adapter.OutboundManager, node adapter.Outbound) bool {
	tag := RealTag(node, N.NetworkTCP)
	if tag == "" {
		tag = node.Tag()
	}
	if ctx != nil {
		if endpoints := service.FromContext[adapter.EndpointManager](ctx); endpoints != nil {
			if _, isEndpoint := endpoints.Get(tag); isEndpoint {
				return true
			}
		}
	}
	if outbounds != nil {
		if leaf, loaded := outbounds.Outbound(tag); loaded {
			node = leaf
		} else if _, isGroup := node.(adapter.OutboundGroup); isGroup {
			return false
		}
	}
	if dialer, isDialer := node.(adapter.DestinationDialerLx); isDialer && dialer.DialsDestinationLx() {
		return true
	}
	return node.Type() == C.TypeDirect
}

// lx:end SPEC 054 / SPEC 138
