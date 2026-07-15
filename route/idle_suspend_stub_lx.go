//go:build !with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	E "github.com/sagernet/sing/common/exceptions"
)

// startIdleSuspend is the no-`with_lx_idle_suspend` stub. The idle-suspend tick
// (SPEC 020) is a mobile-only power/RAM feature — it only pays off where the
// recv-worker bufsArrs are large (Android/iOS, BatchSize=128), so it is gated
// behind the build tag and included only in mobile builds.
//
// If `route.lx_idle_suspend` is set (> 0) but the binary was built without the
// tag, we fail loudly at start rather than silently ignoring the option — a
// silent no-op would hide a misconfigured build. When the option is unset the
// stub is a clean no-op and the endpoint idle machinery (stampActivity /
// resumeOnDial) stays dormant: idleAsleep is never set, so resumeOnDial always
// takes its fast path and the hot dial path is unaffected.
func (r *Router) startIdleSuspend() error {
	if r.idleSuspend > 0 || r.idleSuspendReachable > 0 {
		return E.New("route.lx_idle_suspend is set but this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)")
	}
	return nil
}

// stopIdleSuspend is a no-op without the tag (no tick goroutine was ever started).
func (r *Router) stopIdleSuspend() {}

// OutboundReachable implements adapter.ReachabilityReporter. Without the tag no
// endpoint is ever idle-suspended, so probe gating must never engage: everything
// reports reachable.
func (r *Router) OutboundReachable(tag string) bool { return true }

// lx:end idle-suspend
