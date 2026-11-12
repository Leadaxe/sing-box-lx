//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"time"

	"github.com/sagernet/sing-box/adapter"
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
// An outbound is "reachable" if traffic can currently reach it. The set is
// upstream's: ReferenceManager computes it on every event that reshapes the
// active tree and publishes it to the router (reference_lx.go). The idle tick
// suspends only WG/AWG endpoints outside it; between events the tick reads the
// published map and does one idle comparison per endpoint.

// reachableOutbounds returns the published reachable set, nil before the
// reference manager's first update.
func (r *Router) reachableOutbounds() map[string]bool {
	snapshot := r.referenceLx.Load()
	if snapshot == nil {
		return nil
	}
	return snapshot.referenced
}

// SelectionRefs implements adapter.ReachabilityReporter (SPEC 097): the
// incoming edges into tag, from the same published walk. Counted whether or
// not the idle tick runs — lx.wg.build_max works without lx.wg.idle_suspend.
func (r *Router) SelectionRefs(tag string) (manual int, auto int) {
	snapshot := r.referenceLx.Load()
	if snapshot == nil {
		return 0, 0
	}
	counted := snapshot.refs[tag]
	return counted.manual, counted.auto
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

// idleSuspendLoop ticks every period. Each tick reads the published reachable set and
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
			// No walk published yet (the reference manager's first update runs off
			// the start path): nothing is known to be unreachable, skip the tick.
			if reachable := r.reachableOutbounds(); reachable != nil {
				r.suspendIdleEndpoints(reachable)
			}
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
	reachable := r.reachableOutbounds()
	return reachable == nil || reachable[tag]
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
