// lx:begin 050 run-cancellation
// SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART
//
// A run that reached a half-alive node used to be unstoppable: Close() only shut
// down the ticker, batch.Wait() is a plain WaitGroup that ignores context, and
// nothing downstream observed cancellation. The run therefore survived box
// shutdown, holding the whole outbound slice, and every restart added another
// generation. These tests pin the escape: Close() cancels an in-flight run.

package group

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// hangingDialNode models the field case: the node accepts nothing and never
// answers, so DialContext blocks until its context is done. If cancellation does
// not reach here, the test goroutine leaks exactly as it did on device.
type hangingDialNode struct {
	adapter.Outbound
	tag     string
	entered chan struct{}
}

func (n *hangingDialNode) Type() string           { return C.TypeDirect }
func (n *hangingDialNode) Tag() string            { return n.tag }
func (n *hangingDialNode) Network() []string      { return []string{N.NetworkTCP} }
func (n *hangingDialNode) Dependencies() []string { return nil }

func (n *hangingDialNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	select {
	case n.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// newHangingGroup builds a group over a single node that never answers, wired
// the way NewURLTestGroup wires one: an owned context plus its cancel.
func newHangingGroup(t *testing.T) (*URLTestGroup, *hangingDialNode) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	node := &hangingDialNode{tag: "hangs", entered: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	group := &URLTestGroup{
		ctx:            ctx,
		cancel:         cancel,
		outbound:       &fakeManager{byTag: map[string]adapter.Outbound{node.tag: node}},
		logger:         logger.NOP(),
		outbounds:      []adapter.Outbound{node},
		link:           server.URL,
		interval:       time.Minute,
		history:        urltest.NewHistoryStorage(),
		interruptGroup: interrupt.NewGroup(),
		close:          make(chan struct{}),
	}
	return group, node
}

// TestCloseCancelsInFlightRun is the regression for the zombie itself: a run
// blocked on a node that never answers must end when the group is closed.
func TestCloseCancelsInFlightRun(t *testing.T) {
	group, node := newHangingGroup(t)

	done := make(chan struct{})
	go func() {
		group.testNodes(group.ctx, group.outbounds, true)
		close(done)
	}()

	select {
	case <-node.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the test run never reached the node")
	}

	select {
	case <-done:
		t.Fatal("run finished before Close: the node is supposed to hang")
	case <-time.After(50 * time.Millisecond):
	}

	if err := group.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run survived Close() — this is the zombie that outlives box shutdown")
	}
}

// TestCloseWithoutTickerStillCancels guards the early return in Close: the run
// started by PostStart has no ticker armed, and that is precisely the run that
// used to survive.
func TestCloseWithoutTickerStillCancels(t *testing.T) {
	group, node := newHangingGroup(t)
	if group.ticker != nil {
		t.Fatal("test precondition: no ticker should be armed")
	}

	done := make(chan struct{})
	go func() {
		group.testNodes(group.ctx, group.outbounds, true)
		close(done)
	}()

	select {
	case <-node.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the test run never reached the node")
	}

	if err := group.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close returned early on the nil ticker and left the run running")
	}
}

// TestTestNodesHonoursCallerContext pins the batch-context alignment: tasks used
// to hang their per-node timeout off g.ctx, ignoring the context handed to
// testNodes, so a caller's own deadline could not stop a run.
func TestTestNodesHonoursCallerContext(t *testing.T) {
	group, node := newHangingGroup(t)
	t.Cleanup(func() { group.Close() })

	ctx, cancel := context.WithCancel(group.ctx)
	done := make(chan struct{})
	go func() {
		group.testNodes(ctx, group.outbounds, true)
		close(done)
	}()

	select {
	case <-node.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the test run never reached the node")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("testNodes ignored the caller's context")
	}
}

// lx:end 050 run-cancellation
