//go:build with_lxd

package lxd

import "testing"

// pathTable is a fake pathKind: "file" or "dir" per path, nothing else exists.
func pathTable(paths map[string]string) func(string) (bool, bool) {
	return func(path string) (bool, bool) {
		kind, exists := paths[path]
		return exists, kind == "dir"
	}
}

// TestDetectLinuxInit: PID 1 is the honest answer even when the other
// markers disagree (an OpenWrt userland booted under systemd is systemd's
// machine); the file probes are the fallback for an unreadable /proc/1/comm
// (containers, restricted procfs) and for sysvinit under openrc.
func TestDetectLinuxInit(t *testing.T) {
	testCases := []struct {
		name  string
		pid1  string
		paths map[string]string
		want  string
	}{
		{name: "pid1 systemd beats openwrt marker", pid1: "systemd\n", paths: map[string]string{"/etc/openwrt_release": "file"}, want: "systemd"},
		{name: "pid1 procd beats systemd dir", pid1: "procd\n", paths: map[string]string{"/run/systemd/system": "dir"}, want: "procd"},
		{name: "openwrt marker fallback", pid1: "", paths: map[string]string{"/etc/openwrt_release": "file"}, want: "procd"},
		{name: "systemd dir fallback", pid1: "", paths: map[string]string{"/run/systemd/system": "dir"}, want: "systemd"},
		{name: "systemd mark not a directory", pid1: "init\n", paths: map[string]string{"/run/systemd/system": "file"}, want: ""},
		{name: "openrc under sysvinit", pid1: "init\n", paths: map[string]string{"/run/openrc": "dir"}, want: "openrc"},
		{name: "openrc dir fallback", pid1: "", paths: map[string]string{"/run/openrc": "dir"}, want: "openrc"},
		{name: "container shim without marks", pid1: "docker-init\n", want: ""},
		{name: "nothing readable", pid1: "", want: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := detectLinuxInit(linuxInitEnv{pid1Comm: testCase.pid1, pathKind: pathTable(testCase.paths)})
			if got != testCase.want {
				t.Fatalf("init = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestDetectInitSystem walks the table of SPEC 127 §2.2: the host's init and
// the service bit are separate facts.
func TestDetectInitSystem(t *testing.T) {
	testCases := []struct {
		name        string
		env         initSystemEnv
		wantInit    string
		wantService bool
	}{
		{name: "windows service", env: initSystemEnv{goos: "windows", scmService: true}, wantInit: "scm", wantService: true},
		{name: "windows by hand", env: initSystemEnv{goos: "windows", ppid: 1}, wantInit: "scm"},
		{name: "launchd daemon", env: initSystemEnv{goos: "darwin", ppid: 1, xpcService: launchdLabel}, wantInit: "launchd", wantService: true},
		{name: "launchd job of another label", env: initSystemEnv{goos: "darwin", ppid: 1, xpcService: "org.example.wrapper"}, wantInit: "launchd", wantService: true},
		{name: "darwin terminal", env: initSystemEnv{goos: "darwin", ppid: 4242, xpcService: "application.com.apple.Terminal"}, wantInit: "launchd"},
		{name: "darwin pid 1 without a job", env: initSystemEnv{goos: "darwin", ppid: 1}, wantInit: "launchd"},
		{name: "procd service", env: initSystemEnv{goos: "linux", ppid: 1, linux: linuxInitEnv{pid1Comm: "procd\n", pathKind: pathTable(nil)}}, wantInit: "procd", wantService: true},
		{name: "procd host, started by hand", env: initSystemEnv{goos: "linux", ppid: 4242, linux: linuxInitEnv{pid1Comm: "procd\n", pathKind: pathTable(nil)}}, wantInit: "procd"},
		{name: "systemd service", env: initSystemEnv{goos: "linux", ppid: 1, linux: linuxInitEnv{pid1Comm: "systemd\n", pathKind: pathTable(nil)}}, wantInit: "systemd", wantService: true},
		{name: "systemd host, started by hand", env: initSystemEnv{goos: "linux", ppid: 731, linux: linuxInitEnv{pid1Comm: "systemd\n", pathKind: pathTable(nil)}}, wantInit: "systemd"},
		{name: "openrc service", env: initSystemEnv{goos: "linux", ppid: 1, linux: linuxInitEnv{pid1Comm: "init\n", pathKind: pathTable(map[string]string{"/run/openrc": "dir"})}}, wantInit: "openrc", wantService: true},
		{name: "container without init", env: initSystemEnv{goos: "linux", ppid: 1, linux: linuxInitEnv{pid1Comm: "sh\n", pathKind: pathTable(nil)}}},
		{name: "other platform", env: initSystemEnv{goos: "freebsd", ppid: 1, scmService: true, xpcService: "x"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			initSystem, service := detectInitSystem(testCase.env)
			if initSystem != testCase.wantInit || service != testCase.wantService {
				t.Fatalf("init_system/service = %q/%v, want %q/%v", initSystem, service, testCase.wantInit, testCase.wantService)
			}
		})
	}
}

// TestReleasePlatform: the asset names of lx-release.yml, arch settings
// included.
func TestReleasePlatform(t *testing.T) {
	testCases := []struct {
		goos, goarch string
		settings     map[string]string
		want         string
	}{
		{"linux", "amd64", nil, "linux-amd64"},
		{"linux", "arm64", nil, "linux-arm64"},
		{"linux", "arm", map[string]string{"GOARM": "7"}, "linux-armv7"},
		{"linux", "arm", map[string]string{"GOARM": "7,softfloat"}, "linux-armv7"},
		{"linux", "arm", nil, "linux-arm"},
		{"linux", "mipsle", map[string]string{"GOMIPS": "softfloat"}, "linux-mipsle-softfloat"},
		{"linux", "mips", map[string]string{"GOMIPS": "softfloat"}, "linux-mips-softfloat"},
		{"linux", "mips64le", map[string]string{"GOMIPS64": "hardfloat"}, "linux-mips64le-hardfloat"},
		{"linux", "mipsle", nil, "linux-mipsle"},
		{"windows", "amd64", nil, "windows-amd64"},
		{"windows", "386", nil, "windows-386-legacy-windows-7"},
		{"darwin", "arm64", nil, "darwin-arm64"},
	}
	for _, testCase := range testCases {
		if got := releasePlatform(testCase.goos, testCase.goarch, testCase.settings); got != testCase.want {
			t.Fatalf("%s/%s %v = %q, want %q", testCase.goos, testCase.goarch, testCase.settings, got, testCase.want)
		}
	}
}
