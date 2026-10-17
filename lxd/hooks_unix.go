//go:build with_lxd && unix

package lxd

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/unix"
)

const hooksSupported = true

// runHookProcess runs one hook: the file directly (no shell — the admin's
// shell is inside the file via #!), cwd /, stdin /dev/null, stdout and stderr
// to the log file, in its own process group so a timeout can kill the whole
// tree. The output goes to a FILE, not a pipe: with a pipe exec.Cmd.Wait
// would wait for the pipe to close, and a backgrounded child of the script
// would hold the daemon until the timeout.
func runHookProcess(path string, env []string, logPath string, timeout time.Duration) (int, bool, error) {
	output, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, false, E.Cause(err, "open hook log")
	}
	defer output.Close()
	command := exec.Command(path)
	command.Dir = "/"
	command.Env = append(os.Environ(), env...)
	command.Stdin = nil
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Otherwise the hook (or its backgrounded child) inherits the core's TUN
	// fd and holds the interface until EBUSY on the next start (SPEC 128).
	markInheritableCloseOnExec()
	if err = command.Start(); err != nil {
		return 0, false, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return 0, true, nil
	}
	if err == nil {
		return 0, false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), false, nil
		}
		return exitErr.ExitCode(), false, nil
	}
	return 0, false, err
}

// withHookLock holds an exclusive flock on <hooks>/.lock for the duration of
// one hook. The reason is the race at startRetryingBusy: procd on `restart`
// (and `update` is one) starts the new daemon before the old one finished its
// teardown; without the lock the new post_start would snapshot a system DNS
// that still holds the VPN DNS, because the old post_stop has not restored it
// yet. The descriptor is close-on-exec, so the hook's own children never hold
// the lock after the script exits.
func withHookLock(lockPath string, run func() (int, bool, error)) (int, bool, error) {
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, false, E.Cause(err, "open hook lock")
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return 0, false, E.Cause(err, "lock hooks")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return run()
}
