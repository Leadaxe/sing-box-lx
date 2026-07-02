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

// lx:end idle-suspend
