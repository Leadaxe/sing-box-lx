//go:build with_lxd

package main

import (
	"runtime"

	"github.com/sagernet/sing-box/lxd"
)

// versionExtraLx is the platform card of `sing-box version` (SPEC 127, 129):
// the release asset to download for this machine, the host's init system,
// and the update command where it works.
func versionExtraLx() string {
	platform, initSystem := lxd.PlatformSummary()
	extra := "Platform: " + platform + "\n"
	if initSystem != "" {
		extra += "Init: " + initSystem + "\n"
	}
	if runtime.GOOS == "linux" {
		extra += "Update: sing-box lxd update\n"
	}
	return extra
}
