package transport

// lx: SPEC 139 — the DNS connection pool in a connection test.
//
// The test owns the connections it opened (§5.14): a pooled DNS connection
// that a test's exchange created must not stay parked in the pool for
// production once the test is done with it. The pool marks the connection
// probe-owned when the dial that created it ran on a probe ctx
// (lxtrace.IsProbe); the release that leaves nobody holding it closes it
// instead of parking it. A production query that piggybacked on the shared
// connection meanwhile keeps it until its own release — the mark only decides
// what happens when the last holder lets go.
//
// With fresh=true (§5.6) the test never takes a pooled connection: every
// acquire dials one of its own, outside the shared slot and the idle list,
// closed by its single release. In ConnPoolSingle mode it gets a context of
// its own that ends when the pool forgets it, as the shared one does.
//
// A connection the acquire did not open is not passed off as a new one
// (§5.3): the `dns` stage gets connection=new|reused, and a reused one adds a
// REUSED `connection` child with the connection's age — a live connection
// (reuse=pooled) or one a concurrent query was dialing (reuse=joined).
//
// Without a trace in ctx the helpers cost one ctx.Value lookup; the install
// hook also records when the shared connection was opened (one time.Now per
// connection, no allocation), the age a later test reports.

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
)

type connPoolStateLx[T comparable] struct {
	// owned holds the connections a connection test opened; the value is the
	// cancel of a fresh connection's own context, nil for the others.
	owned map[T]context.CancelCauseFunc
	// sharedSince is when the current shared connection was installed.
	sharedSince time.Time
}

// isFreshLx reports whether the acquire runs for a connection test with
// fresh=true.
func isFreshLx(ctx context.Context) bool {
	return lxtrace.Fresh(ctx)
}

// installedLx is called with p.access held, right where conn is installed
// into state.all by the acquire that dialed it on ctx.
func (p *ConnPool[T]) installedLx(state *connPoolState[T], ctx context.Context, conn T) {
	if state.hasShared && state.shared == conn {
		state.lx.sharedSince = time.Now()
	}
	if lxtrace.From(ctx) == nil {
		return
	}
	lxtrace.Current(ctx).Attr("connection", "new")
	if !lxtrace.IsProbe(ctx) && !lxtrace.Fresh(ctx) {
		return
	}
	if state.lx.owned == nil {
		state.lx.owned = make(map[T]context.CancelCauseFunc)
	}
	state.lx.owned[conn] = nil
}

// releaseProbeOwnedLx reports whether this release must close conn instead of
// parking it: conn is probe-owned and no other holder remains. Must be called
// with p.access held, before Release decides on reuse.
func (p *ConnPool[T]) releaseProbeOwnedLx(state *connPoolState[T], conn T) bool {
	if _, owned := state.lx.owned[conn]; !owned {
		return false
	}
	if p.options.Mode == ConnPoolSingle && state.hasShared && state.shared == conn && state.sharedUsers > 1 {
		return false // another query still runs on it; its release gets here again
	}
	return true
}

// forgetProbeOwnedLx drops the mark when the pool forgets the connection and
// ends a fresh connection's context with the cause. Must be called with
// p.access held.
func (p *ConnPool[T]) forgetProbeOwnedLx(state *connPoolState[T], conn T, cause error) {
	cancel, owned := state.lx.owned[conn]
	if !owned {
		return
	}
	delete(state.lx.owned, conn)
	if cancel != nil {
		cancel(cause)
	}
}

// acquireFreshLx dials a connection for a fresh=true test. dialAndInstall
// tracks it in state.all, so Release and Invalidate find it, and marks it
// owned, so its release closes it; it never becomes the shared connection nor
// enters the idle list.
func (p *ConnPool[T]) acquireFreshLx(ctx context.Context, dial func(context.Context) (T, error)) (T, context.Context, bool, error) {
	var zero T
	p.access.Lock()
	if p.closed {
		p.access.Unlock()
		return zero, nil, false, net.ErrClosed
	}
	current := p.state
	p.access.Unlock()
	conn, created, err := p.dialAndInstall(ctx, current, dial)
	if err != nil {
		return zero, nil, false, err
	}
	lxtrace.Current(ctx).Attr("pool", "detached")
	// Reset and Close end current.ctx, and with it this one; removeConn ends
	// it through the stored cancel.
	var connCtx context.Context
	p.access.Lock()
	if _, tracked := current.lx.owned[conn]; tracked {
		connCtx, current.lx.owned[conn] = context.WithCancelCause(current.ctx)
	}
	p.access.Unlock()
	if connCtx == nil {
		// already forgotten (and closed) by the pool
		var cancel context.CancelCauseFunc
		connCtx, cancel = context.WithCancelCause(current.ctx)
		cancel(net.ErrClosed)
	}
	return conn, connCtx, created, nil
}

// acquireOrderedFreshLx is acquireFreshLx in the shape of acquireOrdered.
func (p *ConnPool[T]) acquireOrderedFreshLx(ctx context.Context, dial func(context.Context) (T, error)) (T, bool, error) {
	conn, _, created, err := p.acquireFreshLx(ctx, dial)
	return conn, created, err
}

// tracePooledLx records an acquire served by a connection that was already
// open: the shared connection or an idle one.
func (p *ConnPool[T]) tracePooledLx(ctx context.Context, state *connPoolState[T], conn T) {
	if lxtrace.From(ctx) == nil {
		return
	}
	p.traceReusedLx(ctx, state, conn, "pooled")
}

// traceCollectedLx records an acquire that waited for a shared connection
// another acquire was dialing: its dial stages belong to that one.
func (p *ConnPool[T]) traceCollectedLx(ctx context.Context, state *connPoolState[T], conn T, startDial bool, err error) {
	if lxtrace.From(ctx) == nil || startDial || err != nil {
		return
	}
	p.traceReusedLx(ctx, state, conn, "joined")
}

func (p *ConnPool[T]) traceReusedLx(ctx context.Context, state *connPoolState[T], conn T, reuse string) {
	var since time.Time
	p.access.Lock()
	if state.hasShared && state.shared == conn {
		since = state.lx.sharedSince
	}
	p.access.Unlock()
	lxtrace.Current(ctx).Attr("connection", "reused")
	var subject string
	if remote, isRemote := any(conn).(interface{ RemoteAddr() net.Addr }); isRemote {
		if addr := remote.RemoteAddr(); addr != nil {
			subject = addr.String()
		}
	}
	_, stage := lxtrace.Begin(ctx, "connection", subject)
	stage.Attr("reuse", reuse)
	if !since.IsZero() {
		stage.Attr("age", strconv.FormatInt(int64(time.Since(since)/time.Second), 10))
	}
	stage.Mark(lxtrace.Reused)
}
