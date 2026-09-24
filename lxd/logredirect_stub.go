//go:build with_lxd && !darwin && !linux && !windows

package lxd

import (
	"os"

	E "github.com/sagernet/sing/common/exceptions"
)

// The remaining platforms have no redirect (Windows has its own, see
// logredirect_windows.go); the daemon runs fine with its output wherever
// the parent pointed it.
const logRotationSupported = false

func redirectStdIO(file *os.File) error {
	return E.New("log rotation is not implemented on this platform")
}
