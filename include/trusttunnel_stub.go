//go:build !with_trusttunnel

package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerTrustTunnelOutbound(registry *outbound.Registry) {
	outbound.Register[option.TrustTunnelOutboundOptions](registry, C.TypeTrustTunnel, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TrustTunnelOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`)
	})
}

func registerMieruInbound(registry *inbound.Registry) {
	inbound.Register[option.TrustTunnelInboundOptions](registry, C.TypeTrustTunnel, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TrustTunnelInboundOptions) (adapter.Inbound, error) {
		return nil, E.New(`trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`)
	})
}
