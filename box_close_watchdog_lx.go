package box

import (
	"github.com/sagernet/sing-box/common/lxclosewatch"
)

// lx: SPEC 030 §8 (issue #50) — a stop that never returns leaves no evidence: the
// platform kills the process before anyone can attach pprof. The watchdog
// writes one full goroutine dump to the core log when the scope close runs
// past the threshold and keeps waiting; it never interrupts the close (SPEC
// 030's "a teardown is never abandoned" stands). The endpoint manager runs a
// second, narrower watchdog over its own close pass that names the endpoints
// still closing (adapter/endpoint/manager_close_lx.go); the package
// deduplicates the dump between the two.
func (s *Box) closeScopeWatched() error {
	watchdog := lxclosewatch.Start(s.logger, "box close", lxclosewatch.DefaultThreshold, nil)
	defer watchdog.Stop()
	return s.scope.Close()
}
