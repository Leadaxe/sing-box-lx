package adapter

import (
	"context"
	"sync/atomic"
)

// lx: SPEC 138 — the node a connection actually went through.
//
// The router creates the connection list entry before a group that picks per
// connection (the urltest group of this fork) has dialed, so the entry cannot
// know the node yet. ActualOutboundLx is a cell the router puts into the
// connection metadata before the trackers see it; every copy of that
// InboundContext (the tracker's, the one NewConnection puts into the context,
// the ExtendContext copies of the outbounds) shares it, so a group writes its
// pick where the connection list reads it.
//
// The cell keeps one pick per group tag: nested groups (round_robin inside
// round_robin) each record their own member, and the reader descends the
// routing chain group by group. A later pick of the same group (a retry
// through another slot) replaces the earlier one.

// ActualOutboundLx is the per-connection cell of picks. The zero value is
// empty; a nil cell ignores writes and reads as empty.
type ActualOutboundLx struct {
	picks atomic.Pointer[ActualPicksLx]
}

// ActualPicksLx is one immutable state of the cell: a short list of
// group tag → picked member, newest first. A reader may keep it as the key of
// what it derived from it.
type ActualPicksLx struct {
	group    string
	outbound Outbound
	next     *ActualPicksLx
}

// Pick records that group picked outbound (a node or a nested group) for this
// connection.
func (c *ActualOutboundLx) Pick(group string, outbound Outbound) {
	if c == nil || outbound == nil {
		return
	}
	for {
		current := c.picks.Load()
		updated := &ActualPicksLx{group: group, outbound: outbound}
		tail := &updated.next
		for entry := current; entry != nil; entry = entry.next {
			if entry.group == group {
				continue
			}
			*tail = &ActualPicksLx{group: entry.group, outbound: entry.outbound}
			tail = &(*tail).next
		}
		if c.picks.CompareAndSwap(current, updated) {
			return
		}
	}
}

// Load returns the current state, nil while no group has picked.
func (c *ActualOutboundLx) Load() *ActualPicksLx {
	if c == nil {
		return nil
	}
	return c.picks.Load()
}

// Picked returns the member group picked for this connection, nil when it has
// not picked (or the state is nil).
func (p *ActualPicksLx) Picked(group string) Outbound {
	for entry := p; entry != nil; entry = entry.next {
		if entry.group == group {
			return entry.outbound
		}
	}
	return nil
}

// RecordPickLx writes a group's pick into the cell of the connection the
// context carries; a context without one (no connection tracker, a multiplex
// session) is left alone.
func RecordPickLx(ctx context.Context, group string, outbound Outbound) {
	if metadata := ContextFrom(ctx); metadata != nil {
		metadata.ActualOutboundLx.Pick(group, outbound)
	}
}

// WithoutActualOutboundLx returns ctx with a copy of its connection metadata that
// carries no cell, or ctx itself when it carries none. A DNS lookup made inside a
// node's dial runs on the connection's context; its transport may dial through
// the same per-connection group, and that pick is not the connection's.
func WithoutActualOutboundLx(ctx context.Context) context.Context {
	metadata := ContextFrom(ctx)
	if metadata == nil || metadata.ActualOutboundLx == nil {
		return ctx
	}
	stripped := *metadata
	stripped.ActualOutboundLx = nil
	return WithContext(ctx, &stripped)
}
