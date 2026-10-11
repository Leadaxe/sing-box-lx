//go:build with_lxd && unix

package lxd

import (
	"os/exec"
	"runtime"
	"syscall"
)

const (
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)

// freeBytes is the space available to this (root) process on dir's filesystem.
func freeBytes(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bfree) * uint64(stat.Bsize), nil
}

func runCombined(name string, args ...string) (string, error) {
	output, err := exec.Command(name, args...).CombinedOutput()
	return string(output), err
}
