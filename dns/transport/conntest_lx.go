package transport

// lx: SPEC 139 §5.14 — a connection test owns the connections it opened: a
// pooled DNS connection that a test's exchange created must not stay parked
// in the pool for production once the test is done with it. The pool marks
// the connection probe-owned when the dial that created it ran on a probe
// ctx (lxtrace.IsProbe); the release that leaves nobody holding it closes it
// instead of parking it. A production query that piggybacked on the shared
// connection meanwhile keeps it until its own release — the mark only decides
// what happens when the last holder lets go. Without a trace in ctx the
// helpers cost one ctx.Value lookup in the dial path and a nil-map read on
// release.

import (
	"context"

	"github.com/sagernet/sing-box/common/lxtrace"
)

// markProbeOwnedLx records conn as opened by a connection test. Must be called
// with p.access held, right where the connection is installed into state.all.
func (p *ConnPool[T]) markProbeOwnedLx(state *connPoolState[T], ctx context.Context, conn T) {
	if !lxtrace.IsProbe(ctx) {
		return
	}
	if state.probeOwned == nil {
		state.probeOwned = make(map[T]struct{})
	}
	state.probeOwned[conn] = struct{}{}
}

// releaseProbeOwnedLx reports whether this release must close conn instead of
// parking it: conn is probe-owned and no other holder remains. Must be called
// with p.access held, before Release decides on reuse.
func (p *ConnPool[T]) releaseProbeOwnedLx(state *connPoolState[T], conn T) bool {
	if _, owned := state.probeOwned[conn]; !owned {
		return false
	}
	if p.options.Mode == ConnPoolSingle && state.hasShared && state.shared == conn && state.sharedUsers > 1 {
		return false // another query still runs on it; its release gets here again
	}
	return true
}

// forgetProbeOwnedLx drops the mark when the pool forgets the connection.
// Must be called with p.access held.
func (p *ConnPool[T]) forgetProbeOwnedLx(state *connPoolState[T], conn T) {
	if state.probeOwned != nil {
		delete(state.probeOwned, conn)
	}
}
