//go:build with_lxd && !unix

package lxd

import (
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// Hooks are unix-only (SPEC 135 §2.5): flock, the root-owned chain and
// process groups are unix notions. daemon.json's key is accepted and warned
// about; no directory, no stubs, no runner.
const hooksSupported = false

func runHookProcess(path string, env []string, logPath string, timeout time.Duration) (int, bool, error) {
	return 0, false, E.New("hooks are not supported on this platform")
}

func withHookLock(lockPath string, run func() (int, bool, error)) (int, bool, error) {
	return run()
}
