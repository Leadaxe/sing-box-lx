//go:build with_lxd && linux

package lxd

import (
	"os"
	"runtime"
)

// linuxInitProbe reads the host; the recipe tests swap it for a fake.
var linuxInitProbe = func() linuxInitEnv {
	return linuxInitEnv{pid1Comm: readPid1Comm(), pathKind: statPathKind}
}

func platformInitSystemEnv() initSystemEnv {
	return initSystemEnv{goos: runtime.GOOS, ppid: os.Getppid(), linux: linuxInitProbe()}
}
