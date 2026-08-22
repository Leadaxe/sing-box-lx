//go:build with_lx_chain

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/chain"
)

// lx: SPEC 073 — outbound `chain` (виртуальная цепочка хопов из групп и узлов).
func registerChainOutbound(registry *outbound.Registry) {
	chain.RegisterOutbound(registry)
}
