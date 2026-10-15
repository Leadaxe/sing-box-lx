//go:build with_lxd

package lxd

import (
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// Init systems named in /admin/info.init_system (SPEC 127): what runs the
// host. A client (the launcher) picks its maintenance recipe — install,
// restart, read the log — by this instead of asking the user.
const (
	initNameProcd   = "procd"
	initNameSystemd = "systemd"
	initNameOpenRC  = "openrc"
	initNameLaunchd = "launchd"
	initNameSCM     = "scm"
)

// platformInfo is the platform card /admin/info serves (SPEC 127), read
// once in Run: the binary's own platform, the host's init system and
// whether this process runs under it as a service. Two separate facts on
// purpose: the recipes depend on the host (a daemon started by hand on a
// systemd box still gets a systemd recipe — to install the service), and
// the service bit is the only heuristic part, so it must not poison them.
type platformInfo struct {
	goos, goarch string
	// platform is the release asset suffix for this binary's platform
	// (linux-armv7, linux-mipsle-softfloat): what a client downloads for
	// this machine. GOARCH alone does not name an asset for arm and mips.
	platform string
	// initSystem is the host's init, "" when none is recognised (a container
	// without one, an unknown platform).
	initSystem string
	// service: this process runs under initSystem as a service.
	service bool
}

// initSystemEnv is what the detection reads from the process and the host;
// the platform fills it (initsystem_<os>.go), the table test by hand.
type initSystemEnv struct {
	goos string
	ppid int
	// xpcService is XPC_SERVICE_NAME: launchd sets it for every job it
	// starts, the LaunchAgent of install-user included.
	xpcService string
	// scmService: the Windows SCM started this process (SPEC 103 §2.11).
	scmService bool
	linux      linuxInitEnv
}

// linuxInitEnv is what the linux host detection reads.
type linuxInitEnv struct {
	// pid1Comm is the content of /proc/1/comm, "" when unreadable.
	pid1Comm string
	// pathKind reports whether a path exists and whether it is a directory.
	pathKind func(path string) (exists bool, isDir bool)
}

// detectInitSystem names the host's init system and tells whether this
// process is its service (SPEC 127 §2.2). Pure: the facts come from env.
//
// The service bit is exact on windows (the SCM says so) and darwin (launchd
// marks its jobs with XPC_SERVICE_NAME) and a heuristic on linux: parent pid
// 1 is where systemd, procd and openrc put their services, but an orphaned
// `nohup sing-box lxd &` lands there too. That is why it is a separate field
// and not folded into initSystem.
func detectInitSystem(env initSystemEnv) (initSystem string, service bool) {
	switch env.goos {
	case "windows":
		return initNameSCM, env.scmService
	case "darwin":
		return initNameLaunchd, env.ppid == 1 && env.xpcService != ""
	case "linux":
		initSystem = detectLinuxInit(env.linux)
		return initSystem, initSystem != "" && env.ppid == 1
	}
	return "", false
}

// detectLinuxInit asks who actually runs the machine. /proc/1/comm is the
// honest answer (it names the live init, not what happens to be installed on
// disk); the file probes only disambiguate when PID 1 is something else
// entirely (a container shim, a supervisor, sysvinit under openrc).
// /run/systemd/system is the same check libsystemd's sd_booted() uses and
// /run/openrc is openrc's equivalent — /run is tmpfs, so a chroot cannot fake
// either. Shared by the `--service` recipe (service_linux.go) and
// /admin/info, so the two never disagree about one host.
func detectLinuxInit(env linuxInitEnv) string {
	switch strings.TrimSpace(env.pid1Comm) {
	case "systemd":
		return initNameSystemd
	case "procd":
		return initNameProcd
	}
	if exists, _ := env.pathKind("/etc/openwrt_release"); exists {
		return initNameProcd
	}
	if _, isDir := env.pathKind("/run/systemd/system"); isDir {
		return initNameSystemd
	}
	if _, isDir := env.pathKind("/run/openrc"); isDir {
		return initNameOpenRC
	}
	return ""
}

// statPathKind is pathKind over the real filesystem, following symlinks.
func statPathKind(path string) (exists bool, isDir bool) {
	info, err := os.Stat(path)
	if err != nil {
		return false, false
	}
	return true, info.IsDir()
}

// readPid1Comm reads /proc/1/comm, "" when it cannot (restricted procfs).
func readPid1Comm() string {
	content, err := os.ReadFile("/proc/1/comm")
	if err != nil {
		return ""
	}
	return string(content)
}

// releasePlatform is the asset suffix lx-release.yml gives the build for
// goos/goarch with the arch settings recorded in the binary (GOARM, GOMIPS,
// GOMIPS64 from debug.BuildInfo). The naming lives in this repository next to
// the workflow that defines it, so it is implemented here once instead of as
// a table in every client.
func releasePlatform(goos, goarch string, settings map[string]string) string {
	switch goarch {
	case "arm":
		// GOARM is "7" or "7,softfloat"; the asset is linux-armv7.
		if major, _, _ := strings.Cut(settings["GOARM"], ","); major != "" {
			return goos + "-armv" + major
		}
	case "mips", "mipsle":
		if float := settings["GOMIPS"]; float != "" {
			return goos + "-" + goarch + "-" + float
		}
	case "mips64", "mips64le":
		if float := settings["GOMIPS64"]; float != "" {
			return goos + "-" + goarch + "-" + float
		}
	case "386":
		// The one windows/386 asset is the Windows 7 build (SPEC 004).
		if goos == "windows" {
			return "windows-386-legacy-windows-7"
		}
	}
	return goos + "-" + goarch
}

// buildSettings reads the arch settings recorded in the running binary.
func buildSettings() map[string]string {
	settings := map[string]string{}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return settings
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOARM", "GOMIPS", "GOMIPS64":
			settings[setting.Key] = setting.Value
		}
	}
	return settings
}

// readPlatformInfo fills the card from the process (SPEC 127), once in Run.
func readPlatformInfo() platformInfo {
	initSystem, service := detectInitSystem(platformInitSystemEnv())
	return platformInfo{
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		platform:   releasePlatform(runtime.GOOS, runtime.GOARCH, buildSettings()),
		initSystem: initSystem,
		service:    service,
	}
}

// PlatformSummary is the platform card for `sing-box version`: the release
// asset of this binary and the host's init system (SPEC 127, 129).
func PlatformSummary() (platform, initSystem string) {
	initSystem, _ = detectInitSystem(platformInitSystemEnv())
	return releasePlatform(runtime.GOOS, runtime.GOARCH, buildSettings()), initSystem
}
