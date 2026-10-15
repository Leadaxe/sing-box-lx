//go:build with_lxd && windows

package lxd

import "runtime"

// platformInitSystemEnv: the daemon under the SCM is the service, the same
// mark as the self-check's serviceContext (SPEC 103 §2.11).
func platformInitSystemEnv() initSystemEnv {
	return initSystemEnv{goos: runtime.GOOS, scmService: IsWindowsService()}
}
