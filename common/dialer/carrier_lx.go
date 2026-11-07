package dialer

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// recordCarrierLx notes the outbound `detour` resolved to as the carrier of
// this dial, for a QUIC client that put a CarrierRecord into ctx (lx: SPEC
// 151). A group is skipped there and records the member it picks itself.
func (d *DetourDialer) recordCarrierLx(ctx context.Context, dialer N.Dialer) {
	if outbound, isOutbound := dialer.(adapter.Outbound); isOutbound {
		adapter.RecordCarrier(ctx, outbound)
	}
}
