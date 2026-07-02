//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// stubSuspendable is a fake WG/AWG endpoint: it implements adapter.IdleSuspendable
// and records every SuspendIfIdle call so a test can assert the tick reached it
// with the right reachable flag.
type stubSuspendable struct {
	adapter.Endpoint
	tag      string
	calls    int
	lastSeen bool // last `reachable` value SuspendIfIdle was called with
}

func (e *stubSuspendable) Tag() string { return e.tag }
func (e *stubSuspendable) SuspendIfIdle(reachable bool, _ time.Duration) {
	e.calls++
	e.lastSeen = reachable
}

// endpointManagerStub is a minimal adapter.EndpointManager exposing a fixed set of
// endpoints; only Endpoints() is implemented (the tick's sole entry point).
type endpointManagerStub struct {
	adapter.EndpointManager
	endpoints []adapter.Endpoint
}

func (m *endpointManagerStub) Endpoints() []adapter.Endpoint { return m.endpoints }

// emptyOutboundManager is an OutboundManager whose Outbounds() is empty — exactly
// the live-box reality for WG/AWG endpoints (they are NOT listed there). This is
// the regression the bug lived in: before the fix the tick iterated ONLY this, so
// it never touched a single endpoint.
type emptyOutboundManager struct {
	adapter.OutboundManager
}

func (m *emptyOutboundManager) Outbounds() []adapter.Outbound { return nil }

// TestIdleTick_iteratesEndpointManager is the integration test for the seam the
// unit tests missed: the tick must find IdleSuspendable endpoints in the ENDPOINT
// manager, because outbound.Outbounds() never lists them. Pre-fix this asserted
// zero calls (tick blind to endpoints); post-fix every endpoint is visited with
// the reachable flag from the supplied set.
func TestIdleTick_iteratesEndpointManager(t *testing.T) {
	wg1 := &stubSuspendable{tag: "wg-1"}
	wg2 := &stubSuspendable{tag: "wg-2"}
	r := &Router{
		outbound:    &emptyOutboundManager{},
		endpoint:    &endpointManagerStub{endpoints: []adapter.Endpoint{wg1, wg2}},
		idleSuspend: 8 * time.Second,
	}

	// wg-1 reachable (e.g. selector Now), wg-2 not.
	r.suspendIdleEndpoints(map[string]bool{"wg-1": true})

	if wg1.calls != 1 || wg2.calls != 1 {
		t.Fatalf("each endpoint must be visited exactly once, got wg-1=%d wg-2=%d", wg1.calls, wg2.calls)
	}
	if !wg1.lastSeen {
		t.Fatalf("wg-1 is reachable; tick must pass reachable=true")
	}
	if wg2.lastSeen {
		t.Fatalf("wg-2 is unreachable; tick must pass reachable=false (would never suspend otherwise)")
	}
}

// TestIdleTick_nilEndpointManagerSafe guards the stub/no-endpoint-manager path:
// suspendIdleEndpoints must not panic when r.endpoint is nil (e.g. a box with no
// endpoints, or a unit test that omits the manager).
func TestIdleTick_nilEndpointManagerSafe(t *testing.T) {
	r := &Router{outbound: &emptyOutboundManager{}, idleSuspend: 8 * time.Second}
	r.suspendIdleEndpoints(map[string]bool{}) // must not panic
}

// suspendableOutbound is an IdleSuspendable that lives in the OUTBOUND manager
// (not the endpoint manager) — to prove the tick scans both lists, even though no
// real outbound type is IdleSuspendable today.
type suspendableOutbound struct {
	adapter.Outbound
	tag      string
	calls    int
	lastSeen bool
}

func (o *suspendableOutbound) Tag() string { return o.tag }
func (o *suspendableOutbound) SuspendIfIdle(reachable bool, _ time.Duration) {
	o.calls++
	o.lastSeen = reachable
}

// outboundManagerWith lists IdleSuspendables in the outbound manager.
type outboundManagerWith struct {
	adapter.OutboundManager
	outbounds []adapter.Outbound
}

func (m *outboundManagerWith) Outbounds() []adapter.Outbound { return m.outbounds }

// TestIdleTick_scansBothManagers proves the tick visits IdleSuspendables in BOTH
// the endpoint manager and the outbound manager, each with the reachable flag keyed
// by its own tag. This locks in the both-lists contract of suspendIdleEndpoints.
func TestIdleTick_scansBothManagers(t *testing.T) {
	ep := &stubSuspendable{tag: "wg-ep"}
	ob := &suspendableOutbound{tag: "ob-susp"}
	r := &Router{
		endpoint:    &endpointManagerStub{endpoints: []adapter.Endpoint{ep}},
		outbound:    &outboundManagerWith{outbounds: []adapter.Outbound{ob}},
		idleSuspend: 8 * time.Second,
	}
	r.suspendIdleEndpoints(map[string]bool{"ob-susp": true}) // ob reachable, ep not

	if ep.calls != 1 || ob.calls != 1 {
		t.Fatalf("both managers must be scanned: ep=%d ob=%d", ep.calls, ob.calls)
	}
	if ep.lastSeen {
		t.Fatalf("wg-ep unreachable → reachable=false expected")
	}
	if !ob.lastSeen {
		t.Fatalf("ob-susp reachable → reachable=true expected")
	}
}

// lx:end idle-suspend
