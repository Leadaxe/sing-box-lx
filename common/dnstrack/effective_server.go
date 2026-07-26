package dnstrack

import (
	"context"
	"sync"
)

// EffectiveServer is a per-request mutable holder for the DNS server that
// ACTUALLY answered a query when the exchange went through a composite
// transport (the lx `group` type, SPEC 035). The client inserts the holder
// into the operation context before calling the transport; the group fills
// it on success; the emit sites prefer it over the composite's own tag —
// otherwise the stream would attribute every answer to the group and
// per-member diagnostics would be impossible.
//
// The holder must live in a context ANCESTOR of the transport call: values
// written by children are invisible upstream, so the group cannot attach it
// itself.
type EffectiveServer struct {
	access      sync.Mutex
	tag         string
	serverType  string
	outboundTag string
}

type effectiveServerKey struct{}

// WithEffectiveServer attaches a fresh holder to ctx. One holder = one
// query operation; reuse across operations would leak attribution between
// concurrent queries.
func WithEffectiveServer(ctx context.Context) context.Context {
	return context.WithValue(ctx, effectiveServerKey{}, &EffectiveServer{})
}

// SetEffectiveServer records the answering member. A no-op when the context
// carries no holder (direct exchanges, tests without the client layer).
func SetEffectiveServer(ctx context.Context, tag string, serverType string, outboundTag string) {
	holder, _ := ctx.Value(effectiveServerKey{}).(*EffectiveServer)
	if holder == nil {
		return
	}
	holder.access.Lock()
	holder.tag = tag
	holder.serverType = serverType
	holder.outboundTag = outboundTag
	holder.access.Unlock()
}

// EffectiveServerFromContext reads the recorded member; ok is false when no
// holder is present or nothing was recorded (cache hits, total failure).
func EffectiveServerFromContext(ctx context.Context) (tag string, serverType string, outboundTag string, ok bool) {
	holder, _ := ctx.Value(effectiveServerKey{}).(*EffectiveServer)
	if holder == nil {
		return
	}
	holder.access.Lock()
	defer holder.access.Unlock()
	return holder.tag, holder.serverType, holder.outboundTag, holder.tag != ""
}
