//go:build with_lxd && darwin

package lxd

import "os"

// platformSelfCheckEnv: the service is told apart by launchd's marks alone,
// for `lxd` and `run` alike (SPEC 100 §2.8), so daemon is not read.
func platformSelfCheckEnv(daemon bool) selfCheckEnv {
	return unixSelfCheckEnv(os.Geteuid(), os.Getppid(), os.Getenv("XPC_SERVICE_NAME"), resolveOwnExecutable, ownerLstat)
}
