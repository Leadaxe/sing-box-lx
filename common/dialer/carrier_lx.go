package dialer

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	N "github.com/sagernet/sing/common/network"
)

// recordCarrierLx notes the outbound `detour` resolved to as the carrier of
// this dial, for a client that put a CarrierRecord into ctx (lx: SPEC 060). A
// group is skipped there and records the member it picks itself.
//
// The returned ctx is the one the carrier is dialled with: it no longer
// carries the sniff guard attempt of the node above, whose handshake is not
// the carrier's own. The record stays reachable, so a group further down
// still writes the member it picks into it.
func (d *DetourDialer) recordCarrierLx(ctx context.Context, dialer N.Dialer) context.Context {
	if outbound, isOutbound := dialer.(adapter.Outbound); isOutbound {
		adapter.RecordCarrier(ctx, outbound)
	}
	return lxsniffguard.WithoutAttempt(ctx)
}

// withoutSniffGuardLx hides the sniff guard attempt and the carrier record of
// a node's dial from the DNS exchange that resolves the node's server (lx:
// SPEC 060): the TLS handshake of a DoH or DoT transport is not the node's
// own, and the transport's detour is not the node's carrier.
func withoutSniffGuardLx(ctx context.Context) context.Context {
	ctx = lxsniffguard.WithoutAttempt(ctx)
	if adapter.CarrierRecordFromContext(ctx) != nil {
		ctx, _ = adapter.ContextWithCarrierRecord(ctx)
	}
	return ctx
}
