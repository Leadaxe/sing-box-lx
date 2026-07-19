// lx:begin idle-suspend
package route

// InvalidateReachability marks the cached reachable set stale so the next idle
// tick recomputes it. Called (via service.FromContext[adapter.ReachabilityInvalidator])
// from the three event points that change the active routing tree — a selector
// switch, a urltest auto-switch, a pool rebuild. Cheap and lock-free; safe to
// call from any goroutine. Implements adapter.ReachabilityInvalidator.
//
// This lives in the always-compiled file (no build tag): the router always
// implements adapter.ReachabilityInvalidator so groups can call it through the
// interface regardless of whether the idle-suspend tick is built in. Without the
// `with_lx_idle_suspend` tag the tick never runs, so nothing reads reachDirty and
// this is a harmless flag store.
func (r *Router) InvalidateReachability() {
	r.reachDirty.Store(true)
}

// QuiesceForShutdown prepares the router for a fast box shutdown (SPEC 030),
// called at the top of box.Close before endpoints are torn down. It stops the
// idle-suspend tick (a no-op without the with_lx_idle_suspend tag) so no new
// urltest/reachability wake can enter an endpoint's resumeOnDial while it is
// closing, and broadcasts a device-pause, which drives every WG/AWG endpoint's
// onPauseUpdated to device.Down() — closing its UDP socket up front so the later
// device.Close() drain (stopping.Wait) returns near-instantly instead of
// blocking on an in-flight ReadFrom. Lives in the always-compiled file so it
// exists in every build. Idempotent and safe on every platform.
func (r *Router) QuiesceForShutdown() {
	r.stopIdleSuspend()
	if r.pauseManager != nil {
		r.pauseManager.DevicePause()
	}
}

// lx:end idle-suspend
