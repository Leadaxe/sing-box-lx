//go:build with_lx_impair

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/impair"
)

// lx: FEATURE 020 / SPEC 164 — test outbound `impair`.
func registerImpairOutbound(registry *outbound.Registry) {
	impair.RegisterOutbound(registry)
}
