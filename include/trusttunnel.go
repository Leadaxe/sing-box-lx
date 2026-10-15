//go:build with_trusttunnel

package include

import (
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/trusttunnel"
)

func registerTrustTunnelInbound(registry *inbound.Registry) {
	trusttunnel.RegisterInbound(registry)
}

func registerTrustTunnelOutbound(registry *outbound.Registry) {
	trusttunnel.RegisterOutbound(registry)
}
