//go:build with_lxd && darwin

package lxd

import (
	"os"
	"syscall"
)

const logRotationSupported = true

// redirectStdIO atomically swaps the DESTINATION of fd 1/2 (dup2): every
// writer — the sagernet log, the core's log, runtime panics — starts landing
// in the file without knowing it. os.Stdout/os.Stderr stay the same *os.File
// over the same fd numbers.
func redirectStdIO(file *os.File) error {
	if err := syscall.Dup2(int(file.Fd()), 1); err != nil {
		return err
	}
	return syscall.Dup2(int(file.Fd()), 2)
}
