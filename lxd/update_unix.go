//go:build with_lxd && unix

package lxd

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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

// memAvailable is the kernel's estimate of memory available without swapping
// (/proc/meminfo MemAvailable); ok=false where there is no such file.
func memAvailable() (uint64, bool) {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	return parseMemAvailable(string(content))
}

func parseMemAvailable(meminfo string) (uint64, bool) {
	for _, line := range strings.Split(meminfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kib << 10, true
		}
	}
	return 0, false
}

// replaceProcess execs path in place of this process — same pid, same stdio,
// so the command's log and exit file (SPEC 128) keep working across it.
func replaceProcess(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}
