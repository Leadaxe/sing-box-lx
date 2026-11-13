package adapter

// lx: SPEC 060 — which outbound carries a dial.
//
// A QUIC client that sends the sniff decoy needs to know the relay that will
// unpack its datagram: the outbound its `detour` names or, when `detour` is a
// group, the member the group picked for this very dial. The dialled conn does
// not say who carried it, so the client puts a CarrierRecord into the dial
// context and the two seams every carried dial passes fill it in: the detour
// dialer (common/dialer/detour.go) with the outbound `detour` resolves to, and
// ResolveChainLeaf (chain_lx.go), which every group calls with the member it
// picked. A group is never recorded, so nested groups resolve down to the
// member; the first non-group write wins, so the member's own `detour` (the
// second hop, which never sees the datagram) does not overwrite the nearest.

import "context"

type carrierRecordKey struct{}

// CarrierRecord receives the type and tag of the nearest non-group outbound a
// dial went through. Empty when the dial used the default dialer (a kernel
// socket) or when no seam recorded anything.
type CarrierRecord struct {
	Type string
	Tag  string
}

// ContextWithCarrierRecord attaches a fresh record to ctx and returns it.
func ContextWithCarrierRecord(ctx context.Context) (context.Context, *CarrierRecord) {
	record := &CarrierRecord{}
	return context.WithValue(ctx, carrierRecordKey{}, record), record
}

// RecordCarrier notes outbound as the carrier of the dial ctx belongs to,
// unless ctx carries no record, outbound is a group, or a carrier is already
// recorded. Nil-safe on both arguments.
func RecordCarrier(ctx context.Context, outbound Outbound) {
	if outbound == nil {
		return
	}
	record, _ := ctx.Value(carrierRecordKey{}).(*CarrierRecord)
	if record == nil || record.Type != "" {
		return
	}
	if _, isGroup := outbound.(OutboundGroup); isGroup {
		return
	}
	// A chain (SPEC 073) is not the relay either: its entry position dials
	// through the same group hook, which records the member picked there.
	if _, isChain := outbound.(ChainPathProvider); isChain {
		return
	}
	record.Type = outbound.Type()
	record.Tag = outbound.Tag()
}

type outboundTagKey struct{}

// ContextWithOutboundTag marks ctx as the build context of the outbound
// tagged tag (set by the outbound registry), for a transport that needs to
// know the node it serves (XHTTP under vless, vmess or trojan asks
// lx.sniff_guard for the node's handle).
func ContextWithOutboundTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, outboundTagKey{}, tag)
}

// OutboundTagFromContext returns the tag ContextWithOutboundTag set, or "".
func OutboundTagFromContext(ctx context.Context) string {
	tag, _ := ctx.Value(outboundTagKey{}).(string)
	return tag
}

// ContextWithExistingCarrierRecord attaches record to ctx, for a layer whose
// dial runs under a context of its own but belongs to the dial that owns
// record (lx: SPEC 060). A nil record leaves ctx as it is.
func ContextWithExistingCarrierRecord(ctx context.Context, record *CarrierRecord) context.Context {
	if record == nil {
		return ctx
	}
	return context.WithValue(ctx, carrierRecordKey{}, record)
}

// CarrierRecordFromContext returns the record ContextWithCarrierRecord put
// into ctx, or nil (lx: SPEC 060).
func CarrierRecordFromContext(ctx context.Context) *CarrierRecord {
	record, _ := ctx.Value(carrierRecordKey{}).(*CarrierRecord)
	return record
}
