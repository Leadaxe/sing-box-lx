//go:build with_lxd && windows

package lxd

import (
	"os/exec"
	"runtime"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)

// freeBytes is never reached: runUpdate refuses before it on a non-linux goos.
func freeBytes(dir string) (uint64, error) {
	return 0, E.New("not supported on windows")
}

func runCombined(name string, args ...string) (string, error) {
	output, err := exec.Command(name, args...).CombinedOutput()
	return string(output), err
}

// memAvailable and replaceProcess are never reached: runUpdate refuses before
// them on a non-linux goos.
func memAvailable() (uint64, bool) { return 0, false }

func replaceProcess(path string, argv []string) error {
	return E.New("not supported on windows")
}
