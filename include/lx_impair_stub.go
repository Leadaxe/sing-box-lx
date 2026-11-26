//go:build !with_lx_impair

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

// lx: FEATURE 020 / SPEC 164 — without the tag `type: impair` is rejected with
// a clear error.
func registerImpairOutbound(registry *outbound.Registry) {
	outbound.Register[option.ImpairOutboundOptions](registry, C.TypeImpair, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ImpairOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`impair outbound is not included in this build, rebuild with -tags with_lx_impair`)
	})
}
