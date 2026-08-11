//go:build with_lx_command && !darwin && !linux

package lxd

import (
	"os"

	E "github.com/sagernet/sing/common/exceptions"
)

// Windows has no dup2, and renaming an open log file fails there anyway —
// rotation needs a write-through design, not an fd swap. Like the service
// install, this waits for a Windows service story; the daemon itself runs
// fine with its output wherever the parent pointed it.
const logRotationSupported = false

func redirectStdIO(file *os.File) error {
	return E.New("log rotation is not implemented on this platform")
}
