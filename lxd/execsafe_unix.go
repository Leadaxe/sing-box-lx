//go:build with_lxd && unix

package lxd

import (
	"os"
	"syscall"

	E "github.com/sagernet/sing/common/exceptions"
)

// lstatOwner reads owner and mode without following a symlink.
func lstatOwner(path string) (ownerInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ownerInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ownerInfo{}, E.New(path, ": no owner information")
	}
	return ownerInfo{uid: stat.Uid, gid: stat.Gid, mode: info.Mode()}, nil
}

// platformSelfCheckEnv: on unix the service is told apart by launchd's marks
// alone, for `lxd` and `run` alike (SPEC 100 §2.8), so daemon is not read.
func platformSelfCheckEnv(daemon bool) selfCheckEnv {
	return unixSelfCheckEnv(os.Geteuid(), os.Getppid(), os.Getenv("XPC_SERVICE_NAME"), resolveOwnExecutable, ownerLstat)
}
