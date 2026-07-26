//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// stubSuspendable is a fake WG/AWG endpoint: it implements adapter.IdleSuspendable
// and records every SuspendIfIdle call so a test can assert the tick reached it
// with the right reachable flag.
type stubSuspendable struct {
	adapter.Endpoint
	tag           string
	calls         int
	lastSeen      bool // last `reachable` value SuspendIfIdle was called with
	teardownCalls int
	lastTeardown  time.Duration // last threshold TeardownIfSlept was called with
}

func (e *stubSuspendable) Tag() string { return e.tag }
func (e *stubSuspendable) SuspendIfIdle(reachable bool, _ time.Duration, _ time.Duration) {
	e.calls++
	e.lastSeen = reachable
}

// lx: SPEC 020 level 3 — part of the IdleSuspendable contract; the tick reaches
// endpoints via a type assertion, so a stub missing this method is invisible to
// it (which is exactly what these tests guard).
func (e *stubSuspendable) TeardownIfSlept(threshold time.Duration) {
	e.teardownCalls++
	e.lastTeardown = threshold
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
func (o *suspendableOutbound) SuspendIfIdle(reachable bool, _ time.Duration, _ time.Duration) {
	o.calls++
	o.lastSeen = reachable
}
func (o *suspendableOutbound) TeardownIfSlept(_ time.Duration) {}

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

// TestIdleTick_passesTeardownThreshold pins the level-3 seam: every tick must
// hand the endpoint BOTH decisions — suspend and teardown — with the configured
// windows. Without this, an interface change silently drops teardown from the
// tick (the type assertion just stops matching).
func TestIdleTick_passesTeardownThreshold(t *testing.T) {
	wg1 := &stubSuspendable{tag: "wg-1"}
	r := &Router{
		outbound:     &emptyOutboundManager{},
		endpoint:     &endpointManagerStub{endpoints: []adapter.Endpoint{wg1}},
		idleSuspend:  30 * time.Second,
		idleTeardown: 5 * time.Minute,
	}
	r.suspendIdleEndpoints(map[string]bool{})

	if wg1.calls != 1 || wg1.teardownCalls != 1 {
		t.Fatalf("each tick must run both decisions once, got suspend=%d teardown=%d",
			wg1.calls, wg1.teardownCalls)
	}
	if wg1.lastTeardown != 5*time.Minute {
		t.Fatalf("teardown must receive the configured window, got %v", wg1.lastTeardown)
	}
}

func teardownPtr(d time.Duration) *badoption.Duration {
	value := badoption.Duration(d)
	return &value
}

// TestIdleTeardownOf_defaultsToReachableWindow: an omitted lx_idle_teardown
// inherits lx_idle_suspend_reachable; an explicit value wins.
func TestIdleTeardownOf_defaultsToReachableWindow(t *testing.T) {
	inherited := idleTeardownOf(option.RouteOptions{
		LXIdleSuspendReachable: badoption.Duration(5 * time.Minute),
	})
	if inherited != 5*time.Minute {
		t.Fatalf("omitted teardown must default to the reachable window, got %v", inherited)
	}
	explicit := idleTeardownOf(option.RouteOptions{
		LXIdleSuspendReachable: badoption.Duration(5 * time.Minute),
		LXIdleTeardown:         teardownPtr(time.Hour),
	})
	if explicit != time.Hour {
		t.Fatalf("explicit teardown must win, got %v", explicit)
	}
	if off := idleTeardownOf(option.RouteOptions{}); off != 0 {
		t.Fatalf("no windows configured → teardown off, got %v", off)
	}
}

// TestIdleTeardownOf_explicitZeroDisables pins the documented kill switch: an
// explicit lx_idle_teardown of "0" means "never tear down", and must NOT inherit
// the reachable window the way an omitted option does. Endpoints then stay
// merely suspended, so the next dial pays ~1 handshake RTT instead of a full
// device+netstack rebuild.
func TestIdleTeardownOf_explicitZeroDisables(t *testing.T) {
	disabled := idleTeardownOf(option.RouteOptions{
		LXIdleSuspend:          badoption.Duration(30 * time.Second),
		LXIdleSuspendReachable: badoption.Duration(5 * time.Minute),
		LXIdleTeardown:         teardownPtr(0),
	})
	if disabled != 0 {
		t.Fatalf(`explicit "0" must disable teardown, got %v`, disabled)
	}
}

// TestIdleTeardownOf_zeroSurvivesConfigRoundTrip guards the option shape itself:
// the kill switch is only real if "0" survives JSON decoding as a present value.
// A non-pointer duration collapses it into the same zero as an absent field,
// which is exactly how the switch was lost before.
func TestIdleTeardownOf_zeroSurvivesConfigRoundTrip(t *testing.T) {
	var options option.RouteOptions
	err := json.Unmarshal([]byte(`{"lx_idle_suspend":"30s","lx_idle_suspend_reachable":"5m","lx_idle_teardown":"0"}`), &options)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if options.LXIdleTeardown == nil {
		t.Fatal(`an explicit "0" must decode as present, not as an absent option`)
	}
	if got := idleTeardownOf(options); got != 0 {
		t.Fatalf(`decoded "0" must disable teardown, got %v`, got)
	}

	var absent option.RouteOptions
	err = json.Unmarshal([]byte(`{"lx_idle_suspend":"30s","lx_idle_suspend_reachable":"5m"}`), &absent)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if absent.LXIdleTeardown != nil {
		t.Fatal("an omitted option must decode as absent")
	}
	if got := idleTeardownOf(absent); got != 5*time.Minute {
		t.Fatalf("omitted teardown must inherit the reachable window, got %v", got)
	}
}

// --- DNS-server detour seeding -------------------------------------------------

type stubDNSTransport struct {
	adapter.DNSTransport
	outboundTag string
}

func (s *stubDNSTransport) OutboundTag() string { return s.outboundTag }

type stubDNSTransportManager struct {
	adapter.DNSTransportManager
	transports []adapter.DNSTransport
}

func (m *stubDNSTransportManager) Transports() []adapter.DNSTransport { return m.transports }

type resolvingOutboundManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *resolvingOutboundManager) Default() adapter.Outbound { return nil }
func (m *resolvingOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, ok := m.byTag[tag]
	return ob, ok
}

// TestComputeReachable_seedsDNSDetours: a WG endpoint used only as a DNS-server
// detour is dialable on every resolution — it must be seeded reachable, or it
// would flap Down/Up around every quiet gap (handshake on the first DNS query
// of each browsing session).
func TestComputeReachable_seedsDNSDetours(t *testing.T) {
	r := &Router{
		outbound: &resolvingOutboundManager{byTag: map[string]adapter.Outbound{}},
		dnsTransport: &stubDNSTransportManager{transports: []adapter.DNSTransport{
			&stubDNSTransport{outboundTag: "wg-dns"},
			&stubDNSTransport{outboundTag: ""}, // default-bound server contributes no seed
		}},
		idleSuspend: 30 * time.Second,
	}
	reachable := r.computeReachable()
	if !reachable["wg-dns"] {
		t.Fatal("a DNS-server detour must be seeded reachable")
	}
}

// TestOutboundReachable_featureOffAlwaysTrue: with idle-suspend off there are no
// suspends, so probe gating must never engage — everything reports reachable.
func TestOutboundReachable_featureOffAlwaysTrue(t *testing.T) {
	r := &Router{} // idleSuspend == 0
	if !r.OutboundReachable("anything") {
		t.Fatal("feature off → OutboundReachable must be true for any tag")
	}
}

// TestStartIdleSuspend_teardownRequiresSuspend pins the prerequisite for the
// teardown kill switch: lx_idle_teardown means nothing without lx_idle_suspend,
// and an explicit "0" must be rejected just like any other value — its resolved
// window is zero, so only the "option was present" flag can catch it.
func TestStartIdleSuspend_teardownRequiresSuspend(t *testing.T) {
	explicitZero := &Router{idleTeardown: 0, idleTeardownSet: true}
	if err := explicitZero.startIdleSuspend(); err == nil {
		t.Fatal(`an explicit lx_idle_teardown "0" without lx_idle_suspend must fail loudly`)
	}
	window := &Router{idleTeardown: 5 * time.Minute, idleTeardownSet: true}
	if err := window.startIdleSuspend(); err == nil {
		t.Fatal("lx_idle_teardown without lx_idle_suspend must fail loudly")
	}
	absent := &Router{}
	if err := absent.startIdleSuspend(); err != nil {
		t.Fatalf("no idle options at all → clean no-op, got %v", err)
	}
}

// TestOutboundReachable_readsCache: with the feature on, the reporter answers
// from the same cached reachable set the tick uses.
func TestOutboundReachable_readsCache(t *testing.T) {
	r := &Router{idleSuspend: 30 * time.Second}
	r.reachMu.Lock()
	r.reachCache = map[string]bool{"in-tree": true}
	r.reachMu.Unlock()
	r.reachDirty.Store(false)
	if !r.OutboundReachable("in-tree") {
		t.Fatal("cached-reachable tag must report true")
	}
	if r.OutboundReachable("orphan") {
		t.Fatal("tag absent from the reachable set must report false")
	}
}

// lx:end idle-suspend
