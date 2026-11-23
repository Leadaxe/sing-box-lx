//go:build with_lxd

package lxd

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// Low-space update (SPEC 129 §2.7): the flow for a router whose flash cannot
// hold a second copy of the binary beside the running one. Nothing is kept on
// the flash twice: the new binary and a gzip copy of the old one live in the
// temp directory (tmpfs = RAM on a router), and the flash holds one binary at
// a time.
//
// Two processes take part. The running (old) binary stages: download, sha256,
// `version`, `check`, the rollback copy — the flash is untouched and any
// failure leaves nothing changed. Then it execs the staged (new) binary with
// --finish-low-space, because a process started from <target> pins that inode:
// unlinking the file would free no space while the updater itself runs from
// it. The staged binary finishes: stop the service, unlink the old binary,
// write the new one, start, health, and restore the old one from its copy
// when the new one does not come up.
//
// The hand-over is a contract between releases — the staging side is the
// installed version, the finishing side the version being installed: the
// arguments built in runLowSpaceUpdate and the two file names below must stay
// readable by every later release. A finishing side that does not understand
// them exits before it changes anything.
//
// What the flow cannot cover: power lost between the unlink and the end of the
// write leaves no binary on the flash and no copy in RAM. The window is the
// flash write, 10-20 s; the flow says so before it stops the service.

const (
	lowSpaceStageName = "sing-box-lxd-update"
	lowSpaceNewName   = "sing-box"
	lowSpaceOldName   = "sing-box.old.gz"
)

// lowSpaceStopWait bounds the wait for the stopped service to exit: procd's
// stop returns at once and gives the process 5 s before SIGKILL.
const lowSpaceStopWait = 15 * time.Second

// LowSpaceFinishOptions are the hand-over arguments of the finishing side.
type LowSpaceFinishOptions struct {
	// StageDir holds the verified new binary and the gzip copy of the old one.
	StageDir string
	// Target is the binary to replace.
	Target string
	// FromVersion is the version being replaced, for the messages.
	FromVersion string
	Service     string
	StateDir    string
}

// serviceStopStart are the init's stop and start commands for a service name.
func serviceStopStart(initSystem, service string) (stop, start []string, ok bool) {
	switch initSystem {
	case initNameProcd:
		script := "/etc/init.d/" + service
		return []string{script, "stop"}, []string{script, "start"}, true
	case initNameSystemd:
		return []string{"systemctl", "stop", service}, []string{"systemctl", "start", service}, true
	case initNameOpenRC:
		return []string{"rc-service", service, "stop"}, []string{"rc-service", service, "start"}, true
	}
	return nil, nil, false
}

func (env updateEnv) stageRoot() string {
	if env.tempDir != "" {
		return env.tempDir
	}
	return os.TempDir()
}

// stageRoom checks the temp directory for size more bytes. tmpfs counts
// against RAM, so the kernel's MemAvailable caps what statfs reports; unlike
// the flash, neither figure is distorted by compression.
func (env updateEnv) stageRoom(size int64) error {
	root := env.stageRoot()
	free, err := env.freeBytes(root)
	if err != nil {
		return E.Cause(err, "free space of ", root)
	}
	where := root
	if env.memAvailable != nil {
		if available, ok := env.memAvailable(); ok && available < free {
			free, where = available, "RAM"
		}
	}
	need := uint64(size) + uint64(size)/10
	if free < need {
		return E.New("low-space mode needs ", need>>10, " KiB free in ", where, ", have ", free>>10)
	}
	return nil
}

// runLowSpaceUpdate is the staging side; roomErr is the failed room check
// that led here, nil when the flow was asked for by --low-space.
func runLowSpaceUpdate(env updateEnv, options UpdateOptions, asset releaseAsset, roomErr error) (int, error) {
	target := env.executable
	if roomErr != nil {
		env.say("no room for a second copy on the flash (%s)", roomErr)
	}
	if options.NoRestart {
		return UpdateExitFailed, E.New("low-space mode replaces the binary only with the service stopped; it cannot honour --no-restart")
	}
	if _, _, known := serviceStopStart(env.initSystem, options.Service); !known {
		return UpdateExitFailed, E.New("low-space mode needs an init it can stop and start (procd, systemd, openrc)")
	}
	if env.replaceProcess == nil {
		return UpdateExitFailed, E.New("low-space mode is not available on this platform")
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return UpdateExitFailed, E.Cause(err, "stat ", target)
	}
	stageDir := filepath.Join(env.stageRoot(), lowSpaceStageName)
	// The new binary (the running one stands in for its size) plus the gzip
	// copy of the old one, which is under half of a plain Go binary.
	if err := env.stageRoom(targetInfo.Size() + targetInfo.Size()/2); err != nil {
		return UpdateExitFailed, err
	}
	env.say("low-space mode: staging in %s, then stop %s, replace %s, start", stageDir, options.Service, target)
	if options.DryRun {
		env.say("dry run: would download %s into %s and replace %s with %s stopped (via %s)", asset.name, stageDir, target, options.Service, env.initSystem)
		return UpdateExitOK, nil
	}

	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return UpdateExitFailed, E.Cause(err, "create ", stageDir)
	}
	fail := func(err error) (int, error) {
		_ = os.RemoveAll(stageDir)
		return UpdateExitFailed, err
	}
	staged := filepath.Join(stageDir, lowSpaceNewName)
	rollback := filepath.Join(stageDir, lowSpaceOldName)

	env.say("downloading %s", asset.name)
	body, size, err := env.fetch("https://github.com/" + updateRepo + "/releases/download/v" + asset.version + "/" + asset.name)
	if err != nil {
		return fail(E.Cause(err, "download ", asset.name))
	}
	err = extractBinary(env.progress(body, asset.name, size), staged, asset.sha256, env.stageRoom)
	_ = body.Close()
	if err != nil {
		return fail(err)
	}
	env.say("sha256 match")
	if err := env.checkNewBinary(staged, options.StateDir); err != nil {
		return fail(err)
	}
	if err := gzipFile(target, rollback); err != nil {
		return fail(E.Cause(err, "keep a copy of the old binary"))
	}
	if info, err := os.Stat(rollback); err == nil {
		env.say("rollback copy of %s: %d KiB in %s", env.version, info.Size()>>10, rollback)
	}

	// The staged binary takes over: this process pins the old inode.
	argv := []string{
		staged, "lxd", "update",
		"--finish-low-space", stageDir,
		"--target", target,
		"--from-version", env.version,
		"--service", options.Service,
	}
	if options.StateDir != "" {
		argv = append(argv, "--state-dir", options.StateDir)
	}
	env.say("handing over to the staged %s", asset.version)
	err = env.replaceProcess(staged, argv)
	return fail(E.Cause(err, "start the staged binary"))
}

// finishLowSpaceUpdate is the finishing side, run by the staged new binary:
// env.version is the version being installed.
func finishLowSpaceUpdate(env updateEnv, options LowSpaceFinishOptions) (int, error) {
	if env.goos != "linux" {
		return UpdateExitFailed, E.New("lxd update works on linux only")
	}
	if env.euid != 0 {
		return UpdateExitFailed, E.New("lxd update must run as root")
	}
	if options.Service == "" {
		options.Service = updateDefaultService
	}
	target := options.Target
	staged := filepath.Join(options.StageDir, lowSpaceNewName)
	rollback := filepath.Join(options.StageDir, lowSpaceOldName)
	for _, path := range []string{staged, rollback, target} {
		if _, err := os.Stat(path); err != nil {
			return UpdateExitFailed, E.Cause(err, "low-space hand-over is incomplete")
		}
	}
	stop, start, known := serviceStopStart(env.initSystem, options.Service)
	_, running, _ := serviceCommands(env.initSystem, options.Service)
	if !known {
		return UpdateExitFailed, E.New("low-space mode needs an init it can stop and start (procd, systemd, openrc)")
	}
	write := env.writeBinary
	if write == nil {
		write = writeFileSynced
	}
	// install puts content at target through <target>.new and a rename: the
	// path never holds a half-written binary.
	install := func(content io.Reader) error {
		newPath := target + ".new"
		if err := write(newPath, content); err != nil {
			_ = os.Remove(newPath)
			return err
		}
		if err := os.Rename(newPath, target); err != nil {
			_ = os.Remove(newPath)
			return E.Cause(err, "replace ", target)
		}
		syncDir(filepath.Dir(target))
		return nil
	}
	// Until the old binary is unlinked nothing has changed: put the service
	// back and leave.
	unchanged := func(err error) (int, error) {
		_, _ = env.run(start[0], start[1:]...)
		_ = os.RemoveAll(options.StageDir)
		return UpdateExitFailed, err
	}

	env.say("stopping %s via %s: the VPN is down for 10-20 s while the flash is written; do not power the device off", options.Service, env.initSystem)
	if output, err := env.run(stop[0], stop[1:]...); err != nil {
		return unchanged(E.Cause(err, "stop ", options.Service, ": ", strings.TrimSpace(output)))
	}
	if !env.waitStopped(running) {
		return unchanged(E.New(options.Service, " is still running ", lowSpaceStopWait, " after stop"))
	}
	if err := os.Remove(target); err != nil {
		return unchanged(E.Cause(err, "remove the old binary"))
	}

	failure := ""
	source, err := os.Open(staged)
	if err == nil {
		err = install(source)
		_ = source.Close()
	}
	if err != nil {
		// Out of space included: a compressing flash (ubifs, jffs2) gives no
		// honest figure in advance, so the write itself is the check.
		failure = "writing " + env.version + " failed: " + err.Error()
	} else {
		env.say("binary replaced: %s is %s", target, env.version)
		env.say("starting %s via %s", options.Service, env.initSystem)
		if output, err := env.run(start[0], start[1:]...); err != nil {
			failure = "start failed: " + strings.TrimSpace(output+" "+err.Error())
		} else if healthy, reason := env.waitHealthy(running, env.version); !healthy {
			failure = reason
		}
	}
	if failure == "" {
		_ = os.RemoveAll(options.StageDir)
		env.say("done: %s is running", env.version)
		return UpdateExitOK, nil
	}

	// Rollback: the old binary fitted before, so it fits again once the new
	// one is gone.
	env.say("%s, rolling back to %s", failure, options.FromVersion)
	_, _ = env.run(stop[0], stop[1:]...)
	env.waitStopped(running)
	_ = os.Remove(target)
	err = gunzipInto(rollback, install)
	if err != nil {
		env.say("the old binary could not be restored: %s", err)
		env.say("both copies are kept in %s; restore by hand: gzip -dc %s > %s && chmod 755 %s && %s",
			options.StageDir, rollback, target, target, strings.Join(start, " "))
		return UpdateExitBroken, E.New(failure, "; rollback to ", options.FromVersion, " failed: ", err.Error())
	}
	_ = os.RemoveAll(options.StageDir)
	_, _ = env.run(start[0], start[1:]...)
	return UpdateExitRolledBack, E.New(failure, ", rolled back to ", options.FromVersion)
}

// waitStopped polls the init until it no longer reports the service running:
// the binary's blocks are freed only when the last process started from it
// has exited.
func (env updateEnv) waitStopped(running []string) bool {
	for attempt := 0; attempt <= int(lowSpaceStopWait/time.Second); attempt++ {
		if attempt > 0 {
			env.sleep(time.Second)
		}
		if _, err := env.run(running[0], running[1:]...); err != nil {
			return true
		}
	}
	return false
}

// gzipFile writes a gzip copy of src to dst. BestSpeed: the copy lives for a
// minute and a router CPU compresses 76 MiB several times faster this way for
// a few MiB more.
func gzipFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writer, _ := gzip.NewWriterLevel(file, gzip.BestSpeed)
	_, err = io.Copy(writer, source)
	if err == nil {
		err = writer.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// gunzipInto feeds the decompressed content of src to sink; the gzip trailer
// (CRC and length) is verified by reading to the end.
func gunzipInto(src string, sink func(io.Reader) error) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	return sink(reader)
}

// writeFileSynced is writeFileFrom plus fsync: in the low-space flow the old
// binary is already gone when the new one is written, so the bytes must be on
// the flash before the rename makes them the binary.
func writeFileSynced(dst string, content io.Reader) error {
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return E.Cause(err, "create ", dst)
	}
	_, err = io.Copy(file, content)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
		return E.Cause(err, "write ", dst)
	}
	return nil
}

// FinishLowSpaceUpdate is the finishing side of the low-space update against
// the real host: the staged binary runs it after the hand-over (SPEC 129 §2.7).
func FinishLowSpaceUpdate(options LowSpaceFinishOptions, out io.Writer) (int, error) {
	env, err := hostUpdateEnv(out)
	if err != nil {
		return UpdateExitFailed, err
	}
	env.coreStatus, env.coreStatusSkip = coreProbe(options.StateDir)
	return finishLowSpaceUpdate(env, options)
}
