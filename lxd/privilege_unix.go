//go:build with_lxd && unix

package lxd

import (
	"os"

	E "github.com/sagernet/sing/common/exceptions"
)

// exeSuffix: unix executables carry no suffix, the copy is sing-box-lxd.
const exeSuffix = ""

// ServiceActionPrivileged reports whether the process may run the system
// service actions (install, copy, uninstall): euid 0 (SPEC 103 §2.15).
func ServiceActionPrivileged() bool {
	return os.Geteuid() == 0
}

// ServiceInstallPrivilegeError is the refusal of an unprivileged
// `--service=install`.
func ServiceInstallPrivilegeError() error {
	return E.New("--service=install needs root (run with sudo); for a per-user agent without sudo use --service=install-user")
}

// PrivilegedCommand renders a command line the operator has to run
// privileged.
func PrivilegedCommand(command string) string {
	return "sudo " + command
}
