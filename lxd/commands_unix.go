//go:build with_lxd && unix

package lxd

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const commandsSupported = true

// spawnDetached starts the wrapper shell in its own session: a service restart
// kills the daemon's process group, not the command. The wrapper redirects the
// command's output to the log and records the exit code itself, so the outcome
// lands on disk whether or not the daemon survives the run. The command line
// travels in the environment, not in the argument list — no quoting layer.
func spawnDetached(dir, name, command string, env []string) (int, error) {
	// The wrapper moves its own variables into shell locals and unsets them,
	// so the command sees only the daemon's LXD_* facts.
	wrapper := exec.Command("/bin/sh", "-c",
		`exec >"$LXD_CMD_LOG" 2>&1; cmd=$LXD_CMD; exit_file=$LXD_CMD_EXIT; unset LXD_CMD LXD_CMD_LOG LXD_CMD_EXIT; /bin/sh -c "$cmd"; echo $? >"$exit_file"`)
	wrapper.Dir = "/"
	wrapper.Env = append(append(os.Environ(), env...),
		"LXD_CMD="+command,
		"LXD_CMD_LOG="+filepath.Join(dir, name+".log"),
		"LXD_CMD_EXIT="+filepath.Join(dir, name+".exit"),
	)
	wrapper.Stdin = nil
	wrapper.Stdout = nil
	wrapper.Stderr = nil
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := wrapper.Start(); err != nil {
		return 0, err
	}
	pid := wrapper.Process.Pid
	// Reap the wrapper when it exits so it does not linger as a zombie; the
	// outcome is read from the files, not from this wait.
	go func() { _ = wrapper.Wait() }()
	return pid, nil
}
