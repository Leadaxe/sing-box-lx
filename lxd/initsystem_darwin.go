//go:build with_lxd && darwin

package lxd

import (
	"os"
	"runtime"
)

func platformInitSystemEnv() initSystemEnv {
	return initSystemEnv{goos: runtime.GOOS, ppid: os.Getppid(), xpcService: os.Getenv("XPC_SERVICE_NAME")}
}
