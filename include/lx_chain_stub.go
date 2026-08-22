//go:build !with_lx_chain

package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// lx: SPEC 073 — без тега `type: chain` отвергается понятной ошибкой; остальное
// ядро по поведению равно upstream (хук ResolveChainLeaf без привязки — no-op).
func registerChainOutbound(registry *outbound.Registry) {
	outbound.Register[option.ChainOutboundOptions](registry, C.TypeChain, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ChainOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`chain outbound is not included in this build, rebuild with -tags with_lx_chain`)
	})
}
