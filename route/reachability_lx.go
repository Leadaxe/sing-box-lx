// lx:begin idle-suspend
package route

import (
	"time"

	"github.com/sagernet/sing-box/adapter"
	R "github.com/sagernet/sing-box/route/rule"
)

// idleTickFloor is the minimum idle-tick period: however small the configured
// threshold, never poll more often than this (the tick itself is a power cost).
const idleTickFloor = 5 * time.Second

// idleTickDivisor sets the tick period to threshold/divisor, so an endpoint is
// caught crossing into "idle for XX" within ~half a window rather than a full one.
const idleTickDivisor = 2

// SPEC 020 — reachability from the active routing tree.
//
// An outbound is "reachable" if traffic can currently reach it: it is the final
// outbound, the target of a routing rule, the active choice of a selector on a
// reachable path, a member of a reachable urltest's current pool, or detoured-to
// (transitively) by any of the above. Idle-suspend (the router idle tick) only
// suspends WG/AWG endpoints that are NOT in this set — an endpoint nothing can
// currently route to.
//
// This is deliberately a fresh walk every tick (no generation cache): the graph
// is tens of tags read from memory, the tick runs every ~XX/2 seconds, and a
// cache would require either invalidation hooks inside upstream selector/urltest
// bodies (rebase cost) or a generation read that is barely cheaper than the walk
// itself. If profiling ever shows the walk is non-trivial, add a generation
// cache then; until then, simplicity wins.

// reachableActiveTags is the narrow capability a urltest group exposes so the
// walk can descend into its WHOLE current pool (round_robin), not just Now().
// Implemented by *group.URLTest; kept as an interface so route does not import
// protocol/group.
type reachableActiveTags interface {
	ActiveTags() []string
}

// ReachableOutbounds returns the set of outbound tags reachable from the active
// routing tree right now. See the package note above for the definition.
func (r *Router) ReachableOutbounds() map[string]bool {
	reachable := make(map[string]bool)

	// Seeds: the final outbound, plus every outbound a routing rule can send to.
	var seeds []string
	if def := r.outbound.Default(); def != nil {
		seeds = append(seeds, def.Tag())
	}
	for _, rule := range r.rules {
		seeds = append(seeds, ruleOutboundTags(rule)...)
	}

	for _, seed := range seeds {
		r.walkReachable(seed, reachable)
	}
	return reachable
}

// ruleOutboundTags extracts the outbound tag(s) a rule's action routes to. Only
// route/bypass actions carry an outbound; everything else (reject, sniff, dns,
// resolve, hijack-dns) routes nowhere and contributes no seed.
func ruleOutboundTags(rule adapter.Rule) []string {
	switch action := rule.Action().(type) {
	case *R.RuleActionRoute:
		if action.Outbound != "" {
			return []string{action.Outbound}
		}
	case *R.RuleActionBypass:
		if action.Outbound != "" {
			return []string{action.Outbound}
		}
	}
	return nil
}

// walkReachable marks tag reachable and descends into whatever tag actively
// routes through right now, transitively. visited (== the result map) guards
// against cycles: a tag already marked is not re-expanded.
func (r *Router) walkReachable(tag string, reachable map[string]bool) {
	if tag == "" || reachable[tag] {
		return
	}
	reachable[tag] = true

	outbound, loaded := r.outbound.Outbound(tag)
	if !loaded {
		return
	}

	// A urltest group routes through its WHOLE active pool (round_robin) or its
	// single current node (legacy). Descend into all of them.
	if active, ok := outbound.(reachableActiveTags); ok {
		for _, child := range active.ActiveTags() {
			r.walkReachable(child, reachable)
		}
		return
	}

	// A selector routes through its single current choice only — Now(), NOT All()
	// (a non-selected member is exactly what we want to be able to suspend).
	if group, ok := outbound.(adapter.OutboundGroup); ok {
		r.walkReachable(group.Now(), reachable)
		return
	}

	// An ordinary outbound routes through its static detour dependencies.
	for _, dependency := range outbound.Dependencies() {
		r.walkReachable(dependency, reachable)
	}
}

// startIdleSuspend launches the idle-suspend tick goroutine. No-op when the
// feature is disabled (idleSuspend == 0) — zero overhead, current behaviour.
func (r *Router) startIdleSuspend() {
	if r.idleSuspend <= 0 {
		return
	}
	r.idleStop = make(chan struct{})
	period := r.idleSuspend / idleTickDivisor
	if period < idleTickFloor {
		period = idleTickFloor
	}
	go r.idleSuspendLoop(period)
}

// stopIdleSuspend stops the idle-suspend tick goroutine, if running.
func (r *Router) stopIdleSuspend() {
	if r.idleStop != nil {
		close(r.idleStop)
		r.idleStop = nil
	}
}

// idleSuspendLoop ticks every period, computes the reachable set once, and asks
// each WG/AWG endpoint to suspend itself if it is unreachable AND idle past the
// threshold. The endpoint owns the suspend/CAS/log decision (keeping the router
// free of protocol/wireguard concrete types).
func (r *Router) idleSuspendLoop(period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-r.idleStop:
			return
		case <-ticker.C:
			reachable := r.ReachableOutbounds()
			for _, outbound := range r.outbound.Outbounds() {
				if suspendable, ok := outbound.(adapter.IdleSuspendable); ok {
					suspendable.SuspendIfIdle(reachable[suspendable.Tag()], r.idleSuspend)
				}
			}
		}
	}
}

// lx:end idle-suspend
