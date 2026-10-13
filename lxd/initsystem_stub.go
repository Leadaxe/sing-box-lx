//go:build with_lxd && !linux && !darwin && !windows

package lxd

import "runtime"

// platformInitSystemEnv: no init system is recognised here.
func platformInitSystemEnv() initSystemEnv {
	return initSystemEnv{goos: runtime.GOOS}
}
