//go:build with_lxd && windows

package lxd

import E "github.com/sagernet/sing/common/exceptions"

// Named commands are unix-only in this iteration: the wrapper is /bin/sh and
// the detachment is a session; daemon.json's key is accepted and warned about.
const commandsSupported = false

func spawnDetached(dir, name, command string, env []string) (int, error) {
	return 0, E.New("commands are not supported on windows")
}
