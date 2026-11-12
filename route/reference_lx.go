// lx:begin idle-suspend
package route

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/service"
)

// SPEC 020 on the 1.15 line: reachability is upstream's. ReferenceManager
// already walks the active routing tree — rules by clash mode, the default
// outbound, DNS servers, inbounds, services and HTTP clients as seeds, a
// selector's current choice, a urltest's picks and pool, detour chains — and
// reruns on every event that reshapes it. The idle tick, the urltest probe
// gate and the build budget read what it publishes here instead of walking the
// tree themselves.

// selectionRefs counts the edges of the active routing tree leading into one
// tag, by class (SPEC 097).
type selectionRefs struct {
	manual int
	auto   int
}

// referenceSnapshotLx is one ReferenceManager.update() result.
type referenceSnapshotLx struct {
	referenced map[string]bool
	refs       map[string]selectionRefs
}

// lxWGManagesEndpoints reports whether lx.wg puts WG/AWG endpoints under the
// idle tick or the build budget.
func (m *ReferenceManager) lxWGManagesEndpoints() bool {
	wg := lxWG(m.ctx)
	return wg.IdleSuspend > 0 || wg.LazyBuild || wg.BuildMax > 0
}

// selfSuspendingLx reports whether the endpoint is suspended by the lx idle
// tick. Upstream seeds every endpoint without on_demand as referenced, since
// it runs whether or not anything routes to it; an endpoint the tick may put
// to sleep is the opposite case and counts only when the tree leads to it.
func (m *ReferenceManager) selfSuspendingLx(endpoint adapter.Endpoint) bool {
	_, suspendable := endpoint.(adapter.IdleSuspendable)
	return suspendable && m.lxWGManagesEndpoints()
}

// publishLx hands the finished walk to the router. manualSeeds are the tags
// the walk started from on the routing side (rule targets, the default
// outbound, static detours); referenced is the full reachable set.
func (m *ReferenceManager) publishLx(manualSeeds []string, referenced map[string]bool) {
	router, isRouter := service.FromContext[adapter.Router](m.ctx).(*Router)
	if !isRouter {
		return
	}
	snapshot := &referenceSnapshotLx{referenced: referenced}
	if m.lxWGManagesEndpoints() {
		snapshot.refs = countSelectionRefs(manualSeeds, referenced, service.FromContext[adapter.OutboundManager](m.ctx).Outbound)
	}
	router.referenceLx.Store(snapshot)
}

// countSelectionRefs derives the SPEC 097 edge counts from a finished walk: one
// pass over the referenced tags, no descent. A seed on the routing side and a
// selector's current choice are manual edges; a urltest's picks and pool, a
// detour and a chain position are auto ones. No weight is inherited: selector →
// urltest → X gives X one auto edge. A referenced tag nothing here points at
// was seeded by a DNS server, an inbound, a service or an HTTP client and gets
// one auto edge.
func countSelectionRefs(manualSeeds []string, referenced map[string]bool, resolve func(tag string) (adapter.Outbound, bool)) map[string]selectionRefs {
	refs := make(map[string]selectionRefs, len(referenced))
	count := func(tag string, manual bool) {
		if tag == "" || !referenced[tag] {
			return
		}
		counted := refs[tag]
		if manual {
			counted.manual++
		} else {
			counted.auto++
		}
		refs[tag] = counted
	}
	for _, seed := range manualSeeds {
		count(seed, true)
	}
	for tag := range referenced {
		outbound, loaded := resolve(tag)
		if !loaded {
			continue
		}
		referrer, isReferrer := outbound.(adapter.Referrer)
		if !isReferrer {
			for _, dependency := range outbound.Dependencies() {
				count(dependency, false)
			}
			continue
		}
		manual := outbound.Type() == C.TypeSelector
		for _, reference := range referrer.References() {
			count(reference, manual)
		}
	}
	for tag := range referenced {
		if _, counted := refs[tag]; !counted {
			refs[tag] = selectionRefs{auto: 1}
		}
	}
	return refs
}

// lx:end idle-suspend
