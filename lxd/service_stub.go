//go:build with_lx_command && !darwin

package lxd

import E "github.com/sagernet/sing/common/exceptions"

// Linux (systemd) and Windows (SCM) service installation are stubs for now:
// today's target is macOS. The daemon itself runs on every platform; only the
// `--service=install` helper is unimplemented here.
func InstallService(daemonArgs []string) error {
	return E.New("lxd: service install is not implemented on this platform yet (macOS only)")
}

func InstallUserService(daemonArgs []string) error {
	return E.New("lxd: service install-user is not implemented on this platform yet (macOS only)")
}

func UninstallService() error {
	return E.New("lxd: service uninstall is not implemented on this platform yet (macOS only)")
}

func PrintService(daemonArgs []string) error {
	return E.New("lxd: service print is not implemented on this platform yet (macOS only)")
}
