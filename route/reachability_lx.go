//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"time"

	"github.com/sagernet/sing-box/adapter"
	R "github.com/sagernet/sing-box/route/rule"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service/pause"
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
// The reachable set is recomputed EVENT-DRIVEN, not every tick: it only changes
// when the active routing tree changes — a selector switch (SelectOutbound), a
// urltest auto-switch / pool rebuild (setSlots), or a config reload. Each of
// those calls InvalidateReachability(), which marks the cache dirty; the next
// idle tick recomputes the walk once and caches it. Between events the tick reads
// the cached map and does a single idle comparison per endpoint — no walk.

// reachableActiveTags is the narrow capability a urltest group exposes so the
// walk can descend into its WHOLE current pool (round_robin), not just Now().
// Implemented by *group.URLTest; kept as an interface so route does not import
// protocol/group.
type reachableActiveTags interface {
	ActiveTags() []string
}

// reachableOutbounds returns the cached reachable set, recomputing the walk only
// when it is dirty (initial state, or after an event). The walk is NOT run under
// the cache lock — it calls into the outbound manager and groups, which take
// their own locks; holding ours across that risks a lock-order deadlock with an
// event point that invalidates while holding a group lock. So: compute outside
// the lock, then publish under it.
func (r *Router) reachableOutbounds() map[string]bool {
	reachable, _ := r.reachability()
	return reachable
}

// reachability returns the cached walk: the reachable set and, per tag, the
// incoming edges counted by class (SPEC 097). Both come from the same walk and
// share the dirty flag.
func (r *Router) reachability() (map[string]bool, map[string]selectionRefs) {
	if !r.reachDirty.Load() {
		r.reachMu.RLock()
		cached, cachedRefs := r.reachCache, r.reachRefs
		r.reachMu.RUnlock()
		if cached != nil {
			return cached, cachedRefs
		}
	}
	// Dirty (or first call): recompute outside any lock. Clear the flag BEFORE the
	// walk so a concurrent event during the walk re-dirties us (we recompute next
	// tick) rather than being lost.
	r.reachDirty.Store(false)
	fresh := r.computeReachability()
	r.reachMu.Lock()
	r.reachCache = fresh.reachable
	r.reachRefs = fresh.refs
	r.reachMu.Unlock()
	return fresh.reachable, fresh.refs
}

// computeReachable runs the actual walk from the seeds (final + rule targets +
// DNS-server detours).
func (r *Router) computeReachable() map[string]bool {
	return r.computeReachability().reachable
}

// computeReachability runs the walk from the seeds: final and rule targets are
// manual roots, DNS-server detours auto ones.
func (r *Router) computeReachability() reachability {
	var seeds []string
	if def := r.outbound.Default(); def != nil {
		seeds = append(seeds, def.Tag())
	}
	for _, rule := range r.rules {
		seeds = append(seeds, ruleOutboundTags(rule)...)
	}
	// A DNS-server detour is dialable at any moment (every resolution goes
	// through it), so it is "traffic can currently reach it" by definition. Not
	// seeding it made a DNS-only WG endpoint flap Down/Up around every quiet gap,
	// adding a wake handshake to the first resolution of each browsing session.
	var dnsSeeds []string
	if r.dnsTransport != nil {
		for _, transport := range r.dnsTransport.Transports() {
			if tag := transport.OutboundTag(); tag != "" {
				dnsSeeds = append(dnsSeeds, tag)
			}
		}
	}
	return walkReachability(seeds, dnsSeeds, r.outbound.Outbound)
}

// SelectionRefs implements adapter.ReachabilityReporter (SPEC 097): the
// incoming edges into tag from the cached walk. Computed whether or not the
// idle tick runs — lx.wg.build_max works without lx.wg.idle_suspend; the event
// points invalidate the cache either way.
func (r *Router) SelectionRefs(tag string) (manual int, auto int) {
	_, refs := r.reachability()
	counted := refs[tag]
	return counted.manual, counted.auto
}

// reachability is the result of one walk: the reachable set and the incoming
// edge counts per tag.
type reachability struct {
	reachable map[string]bool
	refs      map[string]selectionRefs
}

// reachableSet is the pure reachability walk, decoupled from *Router so it is
// unit-testable with a stub resolver. seeds are the entry tags (final + rule
// targets); resolve looks a tag up to an outbound (the second return is false
// for an unknown tag).
func reachableSet(seeds []string, resolve func(tag string) (adapter.Outbound, bool)) map[string]bool {
	return walkReachability(seeds, nil, resolve).reachable
}

// walkReachability walks from the manual seeds (final, rule targets) and the
// auto seeds (DNS-server detours), marking reachable tags and counting every
// edge that leads into a tag by class.
func walkReachability(manualSeeds []string, autoSeeds []string, resolve func(tag string) (adapter.Outbound, bool)) reachability {
	result := reachability{
		reachable: make(map[string]bool),
		refs:      make(map[string]selectionRefs),
	}
	for _, seed := range manualSeeds {
		walkReachable(seed, true, &result, resolve)
	}
	for _, seed := range autoSeeds {
		walkReachable(seed, false, &result, resolve)
	}
	return result
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

// walkReachable counts the edge that led here (manual or auto), marks tag
// reachable and descends into whatever tag actively routes through right now,
// transitively. The edge is counted BEFORE the visited check, so every incoming
// edge counts once; reachable (== the visited set) guards against cycles and
// keeps each tag's own edges from being expanded twice.
//
// Edge classes (SPEC 097): a selector's current choice is manual; a urltest
// pool member, a static dependency (detour, chain position) and a DNS-server
// detour are auto. No weight is inherited: selector → urltest → X gives X one
// auto edge, not a manual one.
func walkReachable(tag string, manual bool, result *reachability, resolve func(tag string) (adapter.Outbound, bool)) {
	if tag == "" {
		return
	}
	counted := result.refs[tag]
	if manual {
		counted.manual++
	} else {
		counted.auto++
	}
	result.refs[tag] = counted
	if result.reachable[tag] {
		return
	}
	result.reachable[tag] = true

	outbound, loaded := resolve(tag)
	if !loaded {
		return
	}

	// A urltest group routes through its WHOLE active pool (round_robin) or its
	// single current node (legacy). Descend into all of them.
	if active, ok := outbound.(reachableActiveTags); ok {
		for _, child := range active.ActiveTags() {
			walkReachable(child, false, result, resolve)
		}
		return
	}

	// A selector routes through its single current choice only — Now(), NOT All()
	// (a non-selected member is exactly what we want to be able to suspend).
	if group, ok := outbound.(adapter.OutboundGroup); ok {
		walkReachable(group.Now(), true, result, resolve)
		return
	}

	// An ordinary outbound routes through its static detour dependencies (a
	// chain outbound lists its positions here).
	for _, dependency := range outbound.Dependencies() {
		walkReachable(dependency, false, result, resolve)
	}
}

// startIdleSuspend launches the idle-suspend tick goroutine. No-op (nil error)
// when the feature is disabled (idleSuspend == 0) — zero overhead. Built only
// with `with_lx_idle_suspend`; the stub build (idle_suspend_stub_lx.go) returns
// an explicit error if the option is set without the tag. Signature returns error
// for symmetry with that stub.
//
// The ticker is registered with the pause.Manager (like the urltest ticker): a
// paused device (screen off / no network) already has every WG device Down'd by
// the pause callbacks, so ticking through the pause is pure waste.
//
// The prerequisite checks repeat option.ResolveLX (SPEC 098), which box.New runs
// first; they stay as a guard for routers built outside box.New.
func (r *Router) startIdleSuspend() error {
	if r.idleSuspend <= 0 {
		if r.idleSuspendReachable > 0 {
			return E.New("lx.wg.idle_suspend_reachable requires lx.wg.idle_suspend")
		}
		if r.idleTeardownSet {
			return E.New("lx.wg.idle_teardown requires lx.wg.idle_suspend")
		}
		return nil
	}
	if r.idleSuspendReachable > 0 && r.idleSuspendReachable < r.idleSuspend {
		return E.New("lx.wg.idle_suspend_reachable must be >= lx.wg.idle_suspend")
	}
	r.idleStop = make(chan struct{})
	period := r.idleSuspend / idleTickDivisor
	if period < idleTickFloor {
		period = idleTickFloor
	}
	ticker := time.NewTicker(period)
	if r.pauseManager != nil {
		r.idlePauseCallback = pause.RegisterTicker(r.pauseManager, ticker, period, nil)
	}
	// The stop channel is passed by value: stopIdleSuspend closes and nils the
	// FIELD, and re-reading the field from the loop would race (a nil channel
	// blocks forever, leaking the goroutine and the ticker).
	go r.idleSuspendLoop(ticker, r.idleStop)
	return nil
}

// stopIdleSuspend stops the idle-suspend tick goroutine, if running.
func (r *Router) stopIdleSuspend() {
	if r.idlePauseCallback != nil {
		r.pauseManager.UnregisterCallback(r.idlePauseCallback)
		r.idlePauseCallback = nil
	}
	if r.idleStop != nil {
		close(r.idleStop)
		r.idleStop = nil
	}
}

// idleSuspendLoop ticks every period. Each tick fetches the reachable set (from
// cache — the walk only re-runs when an event invalidated it, never per tick) and
// asks each WG/AWG endpoint to suspend itself if it is unreachable AND idle past
// the threshold. Per endpoint this is a single map lookup + an atomic idle
// comparison; the endpoint owns the suspend/CAS/log decision.
func (r *Router) idleSuspendLoop(ticker *time.Ticker, stop <-chan struct{}) {
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.suspendIdleEndpoints(r.reachableOutbounds())
		}
	}
}

// OutboundReachable implements adapter.ReachabilityReporter: whether traffic can
// currently reach the outbound with this tag. With the feature off (no tick, no
// suspends) it reports true so callers (the urltest probe gate) never gate.
func (r *Router) OutboundReachable(tag string) bool {
	if r.idleSuspend <= 0 {
		return true
	}
	return r.reachableOutbounds()[tag]
}

// suspendIdleEndpoints runs one tick's per-endpoint suspend decision over every
// IdleSuspendable in the box, given the reachable set.
//
// WG/AWG endpoints live in the ENDPOINT manager, not the outbound manager —
// outbound.Outbounds() never lists them (it returns only m.outbounds; the
// endpoint fallback exists for Outbound(tag) lookups, not the iteration). So the
// tick MUST iterate r.endpoint.Endpoints() to reach them. Outbounds() is also
// scanned for completeness (a future non-endpoint IdleSuspendable, and to keep the
// contract honest), but with no IdleSuspendable outbounds today it is a no-op.
func (r *Router) suspendIdleEndpoints(reachable map[string]bool) {
	suspend := func(suspendable adapter.IdleSuspendable) {
		suspendable.SuspendIfIdle(reachable[suspendable.Tag()], r.idleSuspend, r.idleSuspendReachable)
		// lx: SPEC 020 level 3 — same tick, after the suspend decision: an endpoint
		// that has now been asleep past the teardown window is released entirely.
		// Ordering matters — an endpoint that just fell asleep has slept ~0s, so it
		// is never torn down in the same tick it was suspended.
		suspendable.TeardownIfSlept(r.idleTeardown)
	}
	if r.endpoint != nil {
		for _, endpoint := range r.endpoint.Endpoints() {
			if suspendable, ok := endpoint.(adapter.IdleSuspendable); ok {
				suspend(suspendable)
			}
		}
	}
	for _, outbound := range r.outbound.Outbounds() {
		if suspendable, ok := outbound.(adapter.IdleSuspendable); ok {
			suspend(suspendable)
		}
		// SPEC 073: звенья-endpoint'ы цепочек живут вне менеджера endpoint'ов —
		// тик обязан дотянуться и до них, иначе отсидевший WG-клон держит горячий
		// стек бесконечно. Достижимость — по тегу цепочки-владельца.
		if holder, ok := outbound.(adapter.EndpointCloneHolder); ok {
			for _, endpoint := range holder.CloneEndpoints() {
				if suspendable, ok := endpoint.(adapter.IdleSuspendable); ok {
					suspendable.SuspendIfIdle(reachable[holder.Tag()], r.idleSuspend, r.idleSuspendReachable)
					suspendable.TeardownIfSlept(r.idleTeardown)
				}
			}
		}
	}
}

// lx:end idle-suspend
