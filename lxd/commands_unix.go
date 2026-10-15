//go:build with_lxd && unix

package lxd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
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
	markInheritableCloseOnExec()
	if err := wrapper.Start(); err != nil {
		return 0, err
	}
	pid := wrapper.Process.Pid
	// Reap the wrapper when it exits so it does not linger as a zombie; the
	// outcome is read from the files, not from this wait.
	go func() { _ = wrapper.Wait() }()
	return pid, nil
}

// markInheritableCloseOnExec sets FD_CLOEXEC on every descriptor above stderr
// that lacks it, so a child the daemon spawns does not inherit the core's
// resources. Go opens everything close-on-exec; sing-tun does not — its
// /dev/net/tun fd and the monitor's netlink socket come without the flag,
// and a forked child keeps its own copy of the TUN fd. The kernel holds the
// interface until the last copy closes: a detached `lxd update` spawned
// through `commands` kept lxd-tun0 alive across the service restart, the new
// core got TUNSETIFF EBUSY for longer than its boot retry, went fatal and
// was rolled back (RouteRich, lx.17 → lx.18, 2026-10-13). The flag changes
// nothing in this process — it only closes the descriptor in the child at
// exec — so marking everything is safe; it is done right before each spawn
// because the core may have opened a new TUN since the last one.
func markInheritableCloseOnExec() {
	// Linux lists the open descriptors exactly; without /proc (darwin, where
	// reading /dev/fd stops at the directory's own fd) walk the descriptor
	// space up to the soft limit, capped — Go raises the soft limit to the
	// hard one at startup, which can be millions.
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		for _, entry := range entries {
			if fd, err := strconv.Atoi(entry.Name()); err == nil {
				markCloseOnExec(fd)
			}
		}
		return
	}
	limit := 65536
	var rlimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlimit); err == nil && rlimit.Cur < uint64(limit) {
		limit = int(rlimit.Cur)
	}
	for fd := 3; fd < limit; fd++ {
		markCloseOnExec(fd)
	}
}

// markCloseOnExec adds FD_CLOEXEC to one descriptor; stdio and descriptors
// that are closed or already marked are left alone.
func markCloseOnExec(fd int) {
	if fd <= 2 {
		return
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC != 0 {
		return
	}
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC)
}
