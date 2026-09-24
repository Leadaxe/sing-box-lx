//go:build !with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// startIdleSuspend is the no-`with_lx_idle_suspend` stub. The idle-suspend tick
// (SPEC 020) is a mobile-only power/RAM feature — it only pays off where the
// recv-worker bufsArrs are large (Android/iOS, BatchSize=128), so it is gated
// behind the build tag and included only in mobile builds.
//
// If any lx.wg.* key is set (the idle windows, an explicit idle_teardown
// including "0", lazy_build, build_max, build_overflow "build") but the binary
// was built without the tag, we fail loudly at start rather than silently
// ignoring it — a silent no-op would hide a misconfigured build. When nothing
// is set the stub is a clean no-op and the endpoint idle machinery
// (stampActivity / resumeOnDial) stays dormant: idleAsleep is never set, so
// resumeOnDial always takes its fast path and the hot dial path is unaffected.
func (r *Router) startIdleSuspend() error {
	wg := lxWG(r.ctx)
	if r.idleSuspend > 0 || r.idleSuspendReachable > 0 || r.idleTeardownSet ||
		wg.LazyBuild || wg.BuildMax > 0 || wg.BuildOverflow != option.LXBuildOverflowWait {
		return E.New("lx.wg.* is set but this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)")
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
