//go:build with_lxd && !darwin && !linux && !windows

package lxd

import E "github.com/sagernet/sing/common/exceptions"

// Service installation is a stub on the remaining platforms: darwin installs
// for real, linux prints a recipe (service_linux.go), Windows installs an SCM
// service (service_windows.go). The daemon itself runs on every platform;
// only the `--service` helper is unimplemented here.
// ServiceInstallIsAdvisory: nothing installs here at all, so the caller must
// not prepare daemon.json or try to pair a client either.
const ServiceInstallIsAdvisory = true

func InstallService(daemonArgs []string, execDir string, dryRun bool) error {
	return E.New("lxd: service install is not implemented on this platform yet (macOS only)")
}

func InstallUserService(daemonArgs []string, dryRun bool) error {
	return E.New("lxd: service install-user is not implemented on this platform yet (macOS only)")
}

func DefaultServiceStateDir(user bool) string {
	return "lxd-state"
}

func UninstallService(purge bool, keepCopy bool, execDir string, dryRun bool) error {
	return E.New("lxd: service uninstall is not implemented on this platform yet (macOS only)")
}

func InstallServiceCopy(execDir string, dryRun bool) error {
	return E.New("lxd: service copy is not implemented on this platform yet (macOS only)")
}

func ServiceStatus(execDir string) (ServiceVerdict, error) {
	return ServiceNotInstalled, E.New("lxd: --service=status is not implemented on this platform yet (macOS only)")
}
