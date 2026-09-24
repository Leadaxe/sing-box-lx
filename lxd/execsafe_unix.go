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
