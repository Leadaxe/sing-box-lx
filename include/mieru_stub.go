//go:build !with_mieru

package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/adapter/inbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerMieruOutbound(registry *outbound.Registry) {
	outbound.Register[option.MieruOutboundOptions](registry, C.TypeMieru, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MieruOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`mieru is not included in this build, rebuild with -tags with_mieru`)
	})
}

func registerMieruInbound(registry *inbound.Registry) {
	inbound.Register[option.MieruInboundOptions](registry, C.TypeMieru, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MieruInboundOptions) (adapter.Inbound, error) {
		return nil, E.New(`mieru is not included in this build, rebuild with -tags with_mieru`)
	})
}
