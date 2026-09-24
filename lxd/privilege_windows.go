//go:build with_lxd && windows

package lxd

import (
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

// exeSuffix: the copy is sing-box-lxd.exe; the sidecar keeps the base name.
const exeSuffix = ".exe"

// ServiceActionPrivileged reports whether the process may run the system
// service actions (install, copy, uninstall): an elevated token. os.Getuid
// is -1 on Windows and says nothing (SPEC 103 §2.15).
func ServiceActionPrivileged() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ServiceInstallPrivilegeError is the refusal of an unelevated
// `--service=install`.
func ServiceInstallPrivilegeError() error {
	return E.New("--service=install needs an elevated token (run as administrator)")
}

// PrivilegedCommand renders a command line the operator has to run
// elevated.
func PrivilegedCommand(command string) string {
	return command + " (run as administrator)"
}
