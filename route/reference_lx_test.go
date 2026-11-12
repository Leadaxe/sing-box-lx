package route

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 020 on the 1.15 line — the reachable set and the SPEC 097 edge
// counts come from upstream's ReferenceManager. These tests run its real
// update() over stub managers and read what the router was handed.

type refOutbound struct {
	adapter.Outbound
	tag  string
	kind string
	deps []string
}

func (o *refOutbound) Tag() string            { return o.tag }
func (o *refOutbound) Type() string           { return o.kind }
func (o *refOutbound) Dependencies() []string { return o.deps }

// refGroup is a selector or a urltest: what it references right now.
type refGroup struct {
	refOutbound
	references []string
}

func (g *refGroup) References() []string { return g.references }

// refEndpoint is an endpoint that always runs (no on_demand).
type refEndpoint struct {
	adapter.Endpoint
	tag  string
	deps []string
}

func (e *refEndpoint) Tag() string            { return e.tag }
func (e *refEndpoint) Type() string           { return C.TypeWireGuard }
func (e *refEndpoint) Dependencies() []string { return e.deps }

// refSuspendableEndpoint is a WG/AWG endpoint under the idle tick.
type refSuspendableEndpoint struct{ refEndpoint }

func (e *refSuspendableEndpoint) SuspendIfIdle(bool, time.Duration, time.Duration) {}
func (e *refSuspendableEndpoint) TeardownIfSlept(time.Duration)                    {}

type refManagers struct {
	outbounds      []adapter.Outbound
	endpoints      []adapter.Endpoint
	defaultOutbond string
}

func (m *refManagers) Outbounds() []adapter.Outbound { return m.outbounds }
func (m *refManagers) Endpoints() []adapter.Endpoint { return m.endpoints }
func (m *refManagers) Default() adapter.Outbound {
	outbound, _ := m.Outbound(m.defaultOutbond)
	return outbound
}

func (m *refManagers) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range m.outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	for _, endpoint := range m.endpoints {
		if endpoint.Tag() == tag {
			return endpoint, true
		}
	}
	return nil, false
}

type refOutboundManager struct {
	adapter.OutboundManager
	managers *refManagers
}

func (m refOutboundManager) Outbounds() []adapter.Outbound { return m.managers.Outbounds() }
func (m refOutboundManager) Default() adapter.Outbound     { return m.managers.Default() }
func (m refOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	return m.managers.Outbound(tag)
}

type refEndpointManager struct {
	adapter.EndpointManager
	managers *refManagers
}

func (m refEndpointManager) Endpoints() []adapter.Endpoint { return m.managers.Endpoints() }

// runReferenceUpdate runs one ReferenceManager.update() and returns the router
// it published to. wg is the resolved lx.wg block.
func runReferenceUpdate(t *testing.T, managers *refManagers, wg option.LXWGResolved, rules ...option.Rule) *Router {
	t.Helper()
	ctx := service.ContextWithPtr(service.ContextWithDefaultRegistry(context.Background()), &option.LXResolved{WG: wg})
	ctx = service.ContextWith[adapter.OutboundManager](ctx, refOutboundManager{managers: managers})
	ctx = service.ContextWith[adapter.EndpointManager](ctx, refEndpointManager{managers: managers})
	ctx = service.ContextWith[adapter.InboundManager](ctx, &referenceInboundManager{})
	ctx = service.ContextWith[adapter.ServiceManager](ctx, &referenceServiceManager{})
	ctx = service.ContextWith[adapter.DNSTransportManager](ctx, &referenceTransportManager{})
	ctx = service.ContextWith[adapter.NetworkManager](ctx, &referenceNetworkManager{})
	router := &Router{idleSuspend: wg.IdleSuspend}
	ctx = service.ContextWith[adapter.Router](ctx, router)
	manager := NewReferenceManager(ctx, log.NewNOPFactory().NewLogger("test"), option.Options{
		Route: &option.RouteOptions{Rules: rules},
	})
	manager.update()
	return router
}

func routeRule(outbound string) option.Rule {
	return option.Rule{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultRule{
			RawDefaultRule: option.RawDefaultRule{Domain: []string{outbound + ".example"}},
			RuleAction: option.RuleAction{
				Action:       C.RuleActionTypeRoute,
				RouteOptions: option.RouteActionOptions{Outbound: outbound},
			},
		},
	}
}

func assertPublished(t *testing.T, router *Router, want ...string) {
	t.Helper()
	snapshot := router.referenceLx.Load()
	if snapshot == nil {
		t.Fatal("the reference manager published nothing")
	}
	reachable := snapshot.referenced
	wanted := make(map[string]bool, len(want))
	for _, tag := range want {
		wanted[tag] = true
		if !reachable[tag] {
			t.Errorf("%s must be reachable", tag)
		}
	}
	for tag := range reachable {
		if !wanted[tag] {
			t.Errorf("%s must not be reachable", tag)
		}
	}
}

func assertSelectionRefs(t *testing.T, router *Router, tag string, manual, auto int) {
	t.Helper()
	got := router.referenceLx.Load().refs[tag]
	if got.manual != manual || got.auto != auto {
		t.Errorf("%s: refs (manual %d, auto %d), want (%d, %d)", tag, got.manual, got.auto, manual, auto)
	}
}

var idleSuspendOn = option.LXWGResolved{IdleSuspend: time.Minute}

// The active tree: final → selector → its current choice only; a rule target
// urltest → its picks; a detour chain. A WG endpoint nothing leads to stays
// out of the set — that is the one the idle tick may suspend.
func TestReferenceLx_activeTree(t *testing.T) {
	managers := &refManagers{
		defaultOutbond: "sel",
		outbounds: []adapter.Outbound{
			&refGroup{refOutbound: refOutbound{tag: "sel", kind: C.TypeSelector}, references: []string{"wg-a"}},
			&refGroup{refOutbound: refOutbound{tag: "auto", kind: C.TypeURLTest}, references: []string{"wg-a", "wg-c"}},
			&refOutbound{tag: "vless", kind: C.TypeVLESS, deps: []string{"wg-d"}},
		},
		endpoints: []adapter.Endpoint{
			&refSuspendableEndpoint{refEndpoint{tag: "wg-a"}},
			&refSuspendableEndpoint{refEndpoint{tag: "wg-b"}},
			&refSuspendableEndpoint{refEndpoint{tag: "wg-c"}},
			&refSuspendableEndpoint{refEndpoint{tag: "wg-d"}},
		},
	}
	router := runReferenceUpdate(t, managers, idleSuspendOn, routeRule("auto"), routeRule("vless"))
	assertPublished(t, router, "sel", "auto", "vless", "wg-a", "wg-c", "wg-d")

	// SPEC 097 edge classes: seeds and a selector's choice are manual, a
	// urltest's picks and a detour are auto, and nothing is inherited.
	assertSelectionRefs(t, router, "sel", 1, 0)
	assertSelectionRefs(t, router, "auto", 1, 0)
	assertSelectionRefs(t, router, "vless", 1, 0)
	assertSelectionRefs(t, router, "wg-a", 1, 1)
	assertSelectionRefs(t, router, "wg-b", 0, 0)
	assertSelectionRefs(t, router, "wg-c", 0, 1)
	assertSelectionRefs(t, router, "wg-d", 0, 1)
}

// A selector → urltest → X chain gives X one auto edge, not a manual one.
func TestReferenceLx_noInheritanceThroughNestedGroup(t *testing.T) {
	managers := &refManagers{
		defaultOutbond: "sel",
		outbounds: []adapter.Outbound{
			&refGroup{refOutbound: refOutbound{tag: "sel", kind: C.TypeSelector}, references: []string{"auto"}},
			&refGroup{refOutbound: refOutbound{tag: "auto", kind: C.TypeURLTest}, references: []string{"x"}},
		},
		endpoints: []adapter.Endpoint{&refSuspendableEndpoint{refEndpoint{tag: "x"}}},
	}
	router := runReferenceUpdate(t, managers, idleSuspendOn)
	assertSelectionRefs(t, router, "sel", 1, 0)
	assertSelectionRefs(t, router, "auto", 1, 0)
	assertSelectionRefs(t, router, "x", 0, 1)
}

// Upstream counts every endpoint without on_demand as referenced, because it
// runs regardless. With lx.wg managing the endpoints that would make each of
// them reachable forever; they are referenced only through the tree. Without
// lx.wg upstream's rule stands, and an endpoint of another kind keeps it
// either way.
func TestReferenceLx_suspendableEndpointIsNotItsOwnSeed(t *testing.T) {
	build := func() *refManagers {
		return &refManagers{
			outbounds: []adapter.Outbound{&refOutbound{tag: "detour", kind: C.TypeVLESS}},
			endpoints: []adapter.Endpoint{
				&refSuspendableEndpoint{refEndpoint{tag: "wg-idle", deps: []string{"detour"}}},
				&refEndpoint{tag: "plain"},
			},
		}
	}
	managed := runReferenceUpdate(t, build(), idleSuspendOn)
	assertPublished(t, managed, "plain")

	budgetOnly := runReferenceUpdate(t, build(), option.LXWGResolved{BuildMax: 2})
	assertPublished(t, budgetOnly, "plain")
	assertSelectionRefs(t, budgetOnly, "wg-idle", 0, 0)

	unmanaged := runReferenceUpdate(t, build(), option.LXWGResolved{})
	assertPublished(t, unmanaged, "plain", "wg-idle", "detour")
}

// A tag the routing side does not point at but the walk still reached (a DNS
// server's detour, an inbound's or a service's reference) keeps one auto edge:
// the build budget must not rank it as free to evict.
func TestCountSelectionRefs_seededElsewhere(t *testing.T) {
	refs := countSelectionRefs(nil, map[string]bool{"wg-dns": true}, func(string) (adapter.Outbound, bool) { return nil, false })
	if got := refs["wg-dns"]; got != (selectionRefs{auto: 1}) {
		t.Fatalf("wg-dns: %+v, want one auto edge", got)
	}
}

// Every incoming edge counts: two selectors on x give x two manual edges.
func TestCountSelectionRefs_everyEdge(t *testing.T) {
	outbounds := map[string]adapter.Outbound{
		"sel-1": &refGroup{refOutbound: refOutbound{tag: "sel-1", kind: C.TypeSelector}, references: []string{"x"}},
		"sel-2": &refGroup{refOutbound: refOutbound{tag: "sel-2", kind: C.TypeSelector}, references: []string{"x"}},
		"x":     &refOutbound{tag: "x", deps: []string{"y", "gone"}},
		"y":     &refOutbound{tag: "y"},
	}
	referenced := map[string]bool{"sel-1": true, "sel-2": true, "x": true, "y": true}
	refs := countSelectionRefs([]string{"sel-1", "sel-2"}, referenced, func(tag string) (adapter.Outbound, bool) {
		outbound, loaded := outbounds[tag]
		return outbound, loaded
	})
	if got := refs["x"]; got != (selectionRefs{manual: 2}) {
		t.Errorf("x: %+v, want two manual edges", got)
	}
	if got := refs["y"]; got != (selectionRefs{auto: 1}) {
		t.Errorf("y: %+v, want one auto edge", got)
	}
	if _, counted := refs["gone"]; counted {
		t.Error("an edge into an unreferenced tag must not be counted")
	}
}
