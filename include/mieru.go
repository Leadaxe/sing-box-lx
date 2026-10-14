//go:build with_mieru

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/mieru"
)

func registerMieruOutbound(registry *outbound.Registry) {
	mieru.RegisterOutbound(registry)
}


func registerMieruInbound(registry *inbound.Registry) {
	mieru.RegisterInbound(registry)
}
