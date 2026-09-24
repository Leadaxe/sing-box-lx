//go:build with_lxd && !unix && !windows

package lxd

import E "github.com/sagernet/sing/common/exceptions"

const exeSuffix = ""

// ServiceActionPrivileged: no service manager is supported here.
func ServiceActionPrivileged() bool {
	return false
}

func ServiceInstallPrivilegeError() error {
	return E.New("--service=install is not supported on this platform")
}

func PrivilegedCommand(command string) string {
	return command
}
