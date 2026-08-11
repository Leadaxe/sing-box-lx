//go:build with_lx_command && linux

package lxd

import (
	"os"
	"syscall"
)

const logRotationSupported = true

// redirectStdIO atomically swaps the DESTINATION of fd 1/2. Dup3, not Dup2:
// linux/arm64 and riscv64 never had the dup2 syscall.
func redirectStdIO(file *os.File) error {
	if err := syscall.Dup3(int(file.Fd()), 1, 0); err != nil {
		return err
	}
	return syscall.Dup3(int(file.Fd()), 2, 0)
}
