//go:build with_lxd && (linux || darwin)

package lxd

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// providerTimeout bounds every external process the telemetry and client
// providers spawn. The owner measured ubus on a live router: ten runs finished
// inside busybox's timer resolution, so this is a hang guard, not a budget.
// Same discipline as execSelfCheck in apply.go.
const providerTimeout = 2 * time.Second

// runProvider executes a helper binary, returning ok=false when the binary is
// absent, fails, or times out. A missing tool is a STATE, not an error: the
// same single branch on the client covers "not this platform" and "not on this
// host", exactly like currentRSS() returning rssUnsupported.
func runProvider(name string, args ...string) (string, bool) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

// netInterfaces is a seam over net.Interfaces() so tests can stand in for the
// host's real interface list.
var netInterfaces = net.Interfaces

// pseudoFilesystems never hold user data and would bury the three mounts that
// matter under thirty that do not. Shared by both platform readers: devfs and
// friends are as uninteresting on macOS as proc is on Linux.
var pseudoFilesystems = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"cgroup": true, "cgroup2": true, "debugfs": true, "tracefs": true,
	"securityfs": true, "pstore": true, "bpf": true, "configfs": true,
	"fusectl": true, "hugetlbfs": true, "mqueue": true, "ramfs": true,
	"binfmt_misc": true, "autofs": true, "efivarfs": true, "nsfs": true,
	"devfs": true, "ctlfs": true, "fdesc": true, "procfs": true,
}
