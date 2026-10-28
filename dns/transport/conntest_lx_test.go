//go:build with_lx_command

package transport

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 — the DNS connection pool in a connection test: a reused
// connection is REUSED with its age, fresh=true dials a connection of the
// test's own that is closed on release, and without a trace nothing changes.

type poolTestConn struct {
	id     int
	closed atomic.Bool
}

func (c *poolTestConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 53), Port: 853}
}

type poolTestDialer struct {
	dials atomic.Int32
	gate  chan struct{} // when set, every dial waits for it
}

func (d *poolTestDialer) dial(ctx context.Context) (*poolTestConn, error) {
	if d.gate != nil {
		select {
		case <-d.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &poolTestConn{id: int(d.dials.Add(1))}, nil
}

func newPoolTestPool(mode ConnPoolMode) *ConnPool[*poolTestConn] {
	return NewConnPool(ConnPoolOptions[*poolTestConn]{
		Mode:    mode,
		IsAlive: func(conn *poolTestConn) bool { return !conn.closed.Load() },
		Close:   func(conn *poolTestConn, _ error) { conn.closed.Store(true) },
	})
}

// tracedDNS returns a ctx whose current stage is a `dns` stage of a new trace.
func tracedDNS(options lxtrace.Options) (context.Context, *lxtrace.Trace, *lxtrace.Stage) {
	trace := lxtrace.New(options)
	ctx, stage := lxtrace.Begin(lxtrace.With(context.Background(), trace), "dns", "remote")
	return ctx, trace, stage
}

func attrValues(stage lxtrace.StageSnapshot, key string) []string {
	var values []string
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			values = append(values, attr.Value)
		}
	}
	return values
}

func attrValue(stage lxtrace.StageSnapshot, key string) string {
	values := attrValues(stage, key)
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func TestConnPoolSharedReused_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolSingle)
	defer pool.Close()
	var dialer poolTestDialer

	conn, _, created, err := pool.AcquireShared(context.Background(), dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	pool.Release(conn, true)
	pool.access.Lock()
	require.False(t, pool.state.lx.sharedSince.IsZero(), "the install time is recorded without a trace")
	pool.state.lx.sharedSince = time.Now().Add(-5 * time.Second)
	pool.access.Unlock()

	ctx, trace, dnsStage := tracedDNS(lxtrace.Options{Probe: true})
	reused, _, created, err := pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, conn, reused)
	pool.Release(reused, true)
	require.False(t, reused.closed.Load(), "a probe does not close a production connection")
	dnsStage.End(nil)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 2)
	require.Equal(t, []string{"reused"}, attrValues(summary.Stages[0], "connection"))
	child := summary.Stages[1]
	require.Equal(t, "connection", child.Kind)
	require.Equal(t, "192.0.2.53:853", child.Subject)
	require.Equal(t, dnsStage.ID(), child.Parent)
	require.Equal(t, lxtrace.Reused, child.Result)
	require.Equal(t, "pooled", attrValue(child, "reuse"))
	age, err := strconv.Atoi(attrValue(child, "age"))
	require.NoError(t, err)
	require.Contains(t, []int{5, 6}, age)
	require.EqualValues(t, 1, dialer.dials.Load())
}

func TestConnPoolSharedNew_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolSingle)
	defer pool.Close()
	var dialer poolTestDialer

	ctx, trace, dnsStage := tracedDNS(lxtrace.Options{Probe: true})
	conn, _, created, err := pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	pool.Release(conn, true)
	require.True(t, conn.closed.Load(), "the probe's own connection is not parked (§5.14)")
	dnsStage.End(nil)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, []string{"new"}, attrValues(summary.Stages[0], "connection"))
}

func TestConnPoolSharedJoined_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolSingle)
	defer pool.Close()
	dialer := poolTestDialer{gate: make(chan struct{})}

	starter := make(chan *poolTestConn)
	go func() {
		conn, _, _, err := pool.AcquireShared(context.Background(), dialer.dial)
		assert.NoError(t, err)
		starter <- conn
	}()
	ctx, trace, dnsStage := tracedDNS(lxtrace.Options{Probe: true})
	joined := make(chan *poolTestConn)
	go func() {
		assert.Eventually(t, func() bool {
			pool.access.Lock()
			defer pool.access.Unlock()
			return pool.state.connecting != nil
		}, time.Second, time.Millisecond)
		conn, _, _, err := pool.AcquireShared(ctx, dialer.dial)
		assert.NoError(t, err)
		joined <- conn
	}()
	require.Eventually(t, func() bool {
		pool.access.Lock()
		defer pool.access.Unlock()
		return pool.state.sharedWaiters == 2
	}, time.Second, time.Millisecond)
	close(dialer.gate)
	first, second := <-starter, <-joined
	require.Same(t, first, second)
	dnsStage.End(nil)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 2)
	require.Equal(t, []string{"reused"}, attrValues(summary.Stages[0], "connection"))
	require.Equal(t, lxtrace.Reused, summary.Stages[1].Result)
	require.Equal(t, "joined", attrValue(summary.Stages[1], "reuse"))
	require.Equal(t, "0", attrValue(summary.Stages[1], "age"))
}

func TestConnPoolSharedFresh_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolSingle)
	defer pool.Close()
	var dialer poolTestDialer

	shared, _, _, err := pool.AcquireShared(context.Background(), dialer.dial)
	require.NoError(t, err)
	pool.Release(shared, true)

	ctx, trace, dnsStage := tracedDNS(lxtrace.Options{Fresh: true, Probe: true})
	fresh, freshCtx, created, err := pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	require.NotSame(t, shared, fresh)
	require.NoError(t, freshCtx.Err())

	production, _, created, err := pool.AcquireShared(context.Background(), dialer.dial)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, shared, production, "the fresh connection never becomes the shared one")
	pool.Release(production, true)

	pool.Release(fresh, true)
	require.True(t, fresh.closed.Load(), "closed after the answer, not parked")
	require.ErrorIs(t, context.Cause(freshCtx), net.ErrClosed)
	require.False(t, shared.closed.Load())
	pool.access.Lock()
	_, tracked := pool.state.all[fresh]
	owned := len(pool.state.lx.owned)
	pool.access.Unlock()
	require.False(t, tracked)
	require.Zero(t, owned)

	// Invalidate ends the fresh connection's context with its cause.
	invalidCause := errors.New("read failed")
	fresh, freshCtx, _, err = pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	pool.Invalidate(fresh, invalidCause)
	require.ErrorIs(t, context.Cause(freshCtx), invalidCause)

	// Acquire (no conn ctx) takes the same path.
	fresh, created, err = pool.Acquire(ctx, dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	require.NotSame(t, shared, fresh)
	pool.Release(fresh, true)
	require.True(t, fresh.closed.Load())
	dnsStage.End(nil)

	require.EqualValues(t, 4, dialer.dials.Load())
	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1, "no REUSED stage: every acquire dialed")
	require.Equal(t, []string{"new", "new", "new"}, attrValues(summary.Stages[0], "connection"))
	require.Equal(t, "detached", attrValue(summary.Stages[0], "pool"))
}

func TestConnPoolOrdered_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolOrdered)
	defer pool.Close()
	var dialer poolTestDialer

	idle, created, err := pool.Acquire(context.Background(), dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	pool.Release(idle, true)

	ctx, trace, dnsStage := tracedDNS(lxtrace.Options{Fresh: true, Probe: true})
	fresh, created, err := pool.Acquire(ctx, dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	require.NotSame(t, idle, fresh, "fresh skips the idle connection")
	pool.Release(fresh, true)
	require.True(t, fresh.closed.Load())
	dnsStage.End(nil)
	trace.Finish(nil)

	ctx, trace, dnsStage = tracedDNS(lxtrace.Options{Probe: true})
	reused, created, err := pool.Acquire(ctx, dialer.dial)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, idle, reused)
	pool.Release(reused, true)
	dnsStage.End(nil)
	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 2)
	require.Equal(t, "pooled", attrValue(summary.Stages[1], "reuse"))
	require.Empty(t, attrValue(summary.Stages[1], "age"), "the ordered pool keeps no install time")

	production, created, err := pool.Acquire(context.Background(), dialer.dial)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, idle, production)
	require.EqualValues(t, 2, dialer.dials.Load())
}

func TestConnPoolNoTrace_LX(t *testing.T) {
	pool := newPoolTestPool(ConnPoolSingle)
	defer pool.Close()
	var dialer poolTestDialer
	ctx := context.Background()

	first, _, created, err := pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	require.True(t, created)
	second, _, created, err := pool.AcquireShared(ctx, dialer.dial)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, first, second)
	pool.Release(first, true)
	pool.Release(second, true)
	require.False(t, first.closed.Load(), "parked for the next query")
	require.EqualValues(t, 1, dialer.dials.Load())

	pool.access.Lock()
	state := pool.state
	pool.access.Unlock()
	require.Zero(t, testing.AllocsPerRun(100, func() {
		_ = isFreshLx(ctx)
		pool.tracePooledLx(ctx, state, first)
		pool.traceCollectedLx(ctx, state, first, false, nil)
	}))
}
