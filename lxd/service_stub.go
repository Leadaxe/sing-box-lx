//go:build with_lxd && !darwin && !linux

package lxd

import E "github.com/sagernet/sing/common/exceptions"

// Windows (SCM) service installation is a stub: darwin installs for real,
// linux prints a recipe (service_linux.go). The daemon itself runs on every
// platform; only the `--service` helper is unimplemented here.
// ServiceInstallIsAdvisory: nothing installs here at all, so the caller must
// not prepare daemon.json or try to pair a client either.
const ServiceInstallIsAdvisory = true

func InstallService(daemonArgs []string, dryRun bool) error {
	return E.New("lxd: service install is not implemented on this platform yet (macOS only)")
}

func InstallUserService(daemonArgs []string, dryRun bool) error {
	return E.New("lxd: service install-user is not implemented on this platform yet (macOS only)")
}

func DefaultServiceStateDir(user bool) string {
	return "lxd-state"
}

func UninstallService(purge bool, dryRun bool) error {
	return E.New("lxd: service uninstall is not implemented on this platform yet (macOS only)")
}
