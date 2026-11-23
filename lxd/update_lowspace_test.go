//go:build with_lxd

package lxd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	E "github.com/sagernet/sing/common/exceptions"
)

// lowSpaceRun drives both sides of a low-space update on a fake host: the
// staging side runs in runUpdate, and the fake exec parses the hand-over
// arguments the way the CLI does and runs the finishing side as the new
// version. tune adjusts the finishing side's world.
type lowSpaceRun struct {
	host       *fakeUpdateHost
	target     string
	tempDir    string
	handedOver bool
	argv       []string
	finishCode int
	finishErr  error
}

func newLowSpaceRun(t *testing.T) *lowSpaceRun {
	t.Helper()
	host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
	return &lowSpaceRun{host: host, target: target, tempDir: t.TempDir()}
}

func (r *lowSpaceRun) stageDir() string { return filepath.Join(r.tempDir, lowSpaceStageName) }

// env is the staging side's world: the flash has no room for a second copy.
func (r *lowSpaceRun) env(tune func(finish *updateEnv)) updateEnv {
	env := r.host.env(r.target)
	env.tempDir = r.tempDir
	env.freeBytes = func(dir string) (uint64, error) {
		if dir == r.tempDir {
			return 1 << 30, nil
		}
		return 1, nil
	}
	env.replaceProcess = func(path string, argv []string) error {
		r.handedOver, r.argv = true, argv
		options := LowSpaceFinishOptions{}
		for i := 3; i+1 < len(argv); i += 2 {
			switch argv[i] {
			case "--finish-low-space":
				options.StageDir = argv[i+1]
			case "--target":
				options.Target = argv[i+1]
			case "--from-version":
				options.FromVersion = argv[i+1]
			case "--service":
				options.Service = argv[i+1]
			case "--state-dir":
				options.StateDir = argv[i+1]
			}
		}
		finish := r.host.env(path)
		finish.version = "1.14.3-lx.16"
		if tune != nil {
			tune(&finish)
		}
		r.finishCode, r.finishErr = finishLowSpaceUpdate(finish, options)
		// A real exec does not return: the staging side ends here.
		panic(lowSpaceHandedOver{})
	}
	return env
}

type lowSpaceHandedOver struct{}

// run is runUpdate up to the hand-over; code and err are the staging side's
// own when it never got that far.
func (r *lowSpaceRun) run(env updateEnv, options UpdateOptions) (code int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, handedOver := recovered.(lowSpaceHandedOver); !handedOver {
				panic(recovered)
			}
		}
	}()
	return runUpdate(env, options)
}

func (r *lowSpaceRun) called(suffix string) int {
	count := 0
	for _, call := range r.host.calls {
		if strings.HasSuffix(call, suffix) {
			count++
		}
	}
	return count
}

func assertNoStage(t *testing.T, r *lowSpaceRun) {
	t.Helper()
	if _, err := os.Stat(r.stageDir()); !os.IsNotExist(err) {
		t.Fatal("the stage directory must be removed")
	}
	if _, err := os.Stat(r.target + ".new"); !os.IsNotExist(err) {
		t.Fatal("leftover .new")
	}
}

func TestLowSpaceUpdateSucceeds(t *testing.T) {
	r := newLowSpaceRun(t)
	// stop: gone at once; start: down for a second, then up and holding.
	r.host.running = []bool{false, false, true, true}
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "last_good.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := r.run(r.env(nil), UpdateOptions{StateDir: stateDir})
	if !r.handedOver {
		t.Fatalf("no hand-over: code %d err %v\n%s", code, err, r.host.out.String())
	}
	if r.finishCode != UpdateExitOK || r.finishErr != nil {
		t.Fatalf("finish: code %d err %v\n%s", r.finishCode, r.finishErr, r.host.out.String())
	}
	if readTarget(t, r.target) != "new core" {
		t.Fatal("the binary was not replaced")
	}
	assertNoStage(t, r)
	staged := filepath.Join(r.stageDir(), lowSpaceNewName)
	wantArgv := []string{
		staged, "lxd", "update", "--finish-low-space", r.stageDir(), "--target", r.target,
		"--from-version", "1.14.3-lx.15", "--service", "sing-box-lxd", "--state-dir", stateDir,
	}
	if strings.Join(r.argv, "\n") != strings.Join(wantArgv, "\n") {
		t.Fatalf("hand-over argv:\n%s\nwant:\n%s", strings.Join(r.argv, "\n"), strings.Join(wantArgv, "\n"))
	}
	want := []string{
		staged + " version",
		staged + " check --disable-color -c " + filepath.Join(stateDir, "last_good.json"),
		"/etc/init.d/sing-box-lxd stop",
		"/etc/init.d/sing-box-lxd running",
		"/etc/init.d/sing-box-lxd start",
		"/etc/init.d/sing-box-lxd running", "/etc/init.d/sing-box-lxd running", "/etc/init.d/sing-box-lxd running",
	}
	if strings.Join(r.host.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(r.host.calls, "\n"), strings.Join(want, "\n"))
	}
	out := r.host.out.String()
	for _, line := range []string{"no room for a second copy on the flash", "low-space mode: staging in", "do not power the device off", "done: 1.14.3-lx.16 is running"} {
		if !strings.Contains(out, line) {
			t.Fatalf("missing %q in\n%s", line, out)
		}
	}
}

// TestLowSpaceForced: --low-space takes the flow with room to spare.
func TestLowSpaceForced(t *testing.T) {
	r := newLowSpaceRun(t)
	r.host.running = []bool{false, true, true}
	env := r.env(nil)
	env.freeBytes = func(string) (uint64, error) { return 1 << 30, nil }
	_, _ = r.run(env, UpdateOptions{LowSpace: true})
	if !r.handedOver || r.finishCode != UpdateExitOK || readTarget(t, r.target) != "new core" {
		t.Fatalf("finish: code %d err %v\n%s", r.finishCode, r.finishErr, r.host.out.String())
	}
	if strings.Contains(r.host.out.String(), "no room for a second copy") {
		t.Fatal("a forced run has no failed room check to report")
	}
}

// TestLowSpaceStageFailuresChangeNothing: everything that can go wrong before
// the hand-over leaves the binary, the service and the temp directory alone.
func TestLowSpaceStageFailuresChangeNothing(t *testing.T) {
	cases := map[string]struct {
		tune    func(r *lowSpaceRun, env *updateEnv, options *UpdateOptions)
		wantErr string
	}{
		"no room in the temp directory": {
			tune: func(r *lowSpaceRun, env *updateEnv, _ *UpdateOptions) {
				env.freeBytes = func(string) (uint64, error) { return 1, nil }
			},
			wantErr: "KiB free in " + "TEMP",
		},
		"RAM caps tmpfs": {
			tune: func(r *lowSpaceRun, env *updateEnv, _ *UpdateOptions) {
				env.memAvailable = func() (uint64, bool) { return 2, true }
			},
			wantErr: "KiB free in RAM",
		},
		"the new binary does not run": {
			tune: func(r *lowSpaceRun, env *updateEnv, _ *UpdateOptions) {
				r.host.runFails[filepath.Join(r.stageDir(), lowSpaceNewName)+" version"] = "Permission denied"
			},
			wantErr: "does not run",
		},
		"--no-restart": {
			tune:    func(r *lowSpaceRun, env *updateEnv, options *UpdateOptions) { options.NoRestart = true },
			wantErr: "--no-restart",
		},
		"unknown init": {
			tune:    func(r *lowSpaceRun, env *updateEnv, _ *UpdateOptions) { env.initSystem = "" },
			wantErr: "needs an init",
		},
		"download fails": {
			tune: func(r *lowSpaceRun, env *updateEnv, _ *UpdateOptions) {
				for url := range r.host.urls {
					if strings.HasSuffix(url, ".tar.gz") {
						delete(r.host.urls, url)
					}
				}
			},
			wantErr: "download",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLowSpaceRun(t)
			env := r.env(nil)
			options := UpdateOptions{}
			tc.tune(r, &env, &options)
			code, err := runUpdate(env, options)
			wantErr := strings.Replace(tc.wantErr, "TEMP", r.tempDir, 1)
			if code != UpdateExitFailed || err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("code %d err %v, want %q", code, err, wantErr)
			}
			if r.handedOver || readTarget(t, r.target) != "old core" {
				t.Fatal("a failed stage must change nothing")
			}
			assertNoStage(t, r)
			for _, call := range r.host.calls {
				if strings.HasSuffix(call, " stop") || strings.HasSuffix(call, " start") {
					t.Fatal("the service must not be touched:", call)
				}
			}
		})
	}
}

func TestLowSpaceDryRun(t *testing.T) {
	r := newLowSpaceRun(t)
	code, err := runUpdate(r.env(nil), UpdateOptions{DryRun: true})
	if code != UpdateExitOK || err != nil || r.handedOver || len(r.host.calls) != 0 {
		t.Fatalf("code %d err %v calls %v", code, err, r.host.calls)
	}
	if !strings.Contains(r.host.out.String(), "dry run: would download sing-box-1.14.3-lx.16-linux-arm64.tar.gz into "+r.stageDir()) {
		t.Fatal(r.host.out.String())
	}
	assertNoStage(t, r)
}

// TestLowSpaceNewBinaryLargerThanOld: the running binary stands in for the new
// one in the first room check; the tar header's size sends the update to the
// low-space flow when only the new one does not fit.
func TestLowSpaceNewBinaryLargerThanOld(t *testing.T) {
	r := newLowSpaceRun(t)
	r.host.running = []bool{false, true, true}
	env := r.env(nil)
	// "old core" (8 bytes) fits with its margin, "new core" plus a byte would not.
	archive, sum := makeArchive(t, []byte("new core, larger"), false)
	name := "sing-box-1.14.3-lx.16-linux-arm64.tar.gz"
	r.host.urls["https://github.com/Leadaxe/sing-box-lx/releases/latest/download/SHA256SUMS"] = []byte(sum + "  " + name + "\n")
	r.host.urls["https://github.com/Leadaxe/sing-box-lx/releases/download/v1.14.3-lx.16/"+name] = archive
	env.freeBytes = func(dir string) (uint64, error) {
		if dir == r.tempDir {
			return 1 << 30, nil
		}
		return 10, nil
	}
	_, _ = r.run(env, UpdateOptions{})
	if !r.handedOver || r.finishCode != UpdateExitOK || readTarget(t, r.target) != "new core, larger" {
		t.Fatalf("finish: code %d err %v\n%s", r.finishCode, r.finishErr, r.host.out.String())
	}
}

// TestLowSpaceRollsBack: every failure after the old binary is gone restores
// it from the gzip copy and starts the service again.
func TestLowSpaceRollsBack(t *testing.T) {
	cases := map[string]struct {
		running []bool
		tune    func(r *lowSpaceRun, finish *updateEnv)
		wantErr string
		// starts: once for the new binary when it got that far, once after the restore.
		wantStarts int
	}{
		"the service does not come up": {
			running: []bool{false}, wantErr: "did not come up, rolled back to 1.14.3-lx.15", wantStarts: 2,
		},
		"the core is fatal": {
			// stop: gone; start: up and holding; rollback stop: gone.
			running: []bool{false, true, true, false},
			tune: func(r *lowSpaceRun, finish *updateEnv) {
				finish.coreStatus = func() (CoreStatus, error) {
					return CoreStatus{Status: "fatal", LastError: "bad config"}, nil
				}
			},
			wantErr: "the core did not start: bad config", wantStarts: 2,
		},
		"the flash is full": {
			running: []bool{false},
			tune: func(r *lowSpaceRun, finish *updateEnv) {
				first := true
				finish.writeBinary = func(dst string, content io.Reader) error {
					if first {
						first = false
						_ = os.WriteFile(dst, []byte("half"), 0o755)
						return E.Cause(syscall.ENOSPC, "write ", dst)
					}
					return writeFileSynced(dst, content)
				}
			},
			wantErr: "no space left on device, rolled back to 1.14.3-lx.15", wantStarts: 1,
		},
		"start fails": {
			running: []bool{false},
			tune: func(r *lowSpaceRun, finish *updateEnv) {
				// Fails on the first call only: the fake cannot tell them apart,
				// so the rollback's start fails too — it is best effort.
				r.host.runFails["/etc/init.d/sing-box-lxd start"] = "exec format error"
			},
			wantErr: "start failed: exec format error", wantStarts: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLowSpaceRun(t)
			r.host.running = tc.running
			env := r.env(func(finish *updateEnv) {
				if tc.tune != nil {
					tc.tune(r, finish)
				}
			})
			_, _ = r.run(env, UpdateOptions{})
			if !r.handedOver || r.finishCode != UpdateExitRolledBack || r.finishErr == nil || !strings.Contains(r.finishErr.Error(), tc.wantErr) {
				t.Fatalf("finish: code %d err %v, want %q\n%s", r.finishCode, r.finishErr, tc.wantErr, r.host.out.String())
			}
			if readTarget(t, r.target) != "old core" {
				t.Fatalf("the old binary must be back, got %q", readTarget(t, r.target))
			}
			if info, _ := os.Stat(r.target); info.Mode().Perm()&0o100 == 0 {
				t.Fatal("the restored binary must be executable")
			}
			assertNoStage(t, r)
			if got := r.called(" start"); got != tc.wantStarts {
				t.Fatalf("start ran %d times, want %d\n%s", got, tc.wantStarts, strings.Join(r.host.calls, "\n"))
			}
		})
	}
}

// TestLowSpaceBrokenRollbackKeepsCopies: when the old binary cannot be put
// back either, the copies stay and the log says how to restore by hand.
func TestLowSpaceBrokenRollbackKeepsCopies(t *testing.T) {
	r := newLowSpaceRun(t)
	r.host.running = []bool{false}
	env := r.env(func(finish *updateEnv) {
		finish.writeBinary = func(dst string, content io.Reader) error {
			return E.Cause(syscall.EIO, "write ", dst)
		}
	})
	_, _ = r.run(env, UpdateOptions{})
	if r.finishCode != UpdateExitBroken || r.finishErr == nil || !strings.Contains(r.finishErr.Error(), "rollback to 1.14.3-lx.15 failed") {
		t.Fatalf("finish: code %d err %v", r.finishCode, r.finishErr)
	}
	rollback := filepath.Join(r.stageDir(), lowSpaceOldName)
	for _, kept := range []string{rollback, filepath.Join(r.stageDir(), lowSpaceNewName)} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatal("the copies must stay:", err)
		}
	}
	hint := "restore by hand: gzip -dc " + rollback + " > " + r.target + " && chmod 755 " + r.target + " && /etc/init.d/sing-box-lxd start"
	if !strings.Contains(r.host.out.String(), hint) {
		t.Fatalf("missing %q in\n%s", hint, r.host.out.String())
	}
	// The copy really is the old binary.
	var restored []byte
	if err := gunzipInto(rollback, func(content io.Reader) (err error) {
		restored, err = io.ReadAll(content)
		return err
	}); err != nil || string(restored) != "old core" {
		t.Fatalf("rollback copy holds %q %v", restored, err)
	}
}

// TestLowSpaceFinishBeforeUnlinkChangesNothing: a stop that fails, a service
// that will not exit and an incomplete hand-over all end with the old binary
// in place and the service started again.
func TestLowSpaceFinishBeforeUnlinkChangesNothing(t *testing.T) {
	t.Run("stop fails", func(t *testing.T) {
		r := newLowSpaceRun(t)
		r.host.runFails["/etc/init.d/sing-box-lxd stop"] = "ubus timeout"
		_, _ = r.run(r.env(nil), UpdateOptions{})
		if r.finishCode != UpdateExitFailed || r.finishErr == nil || readTarget(t, r.target) != "old core" || r.called(" start") != 1 {
			t.Fatalf("code %d err %v\n%s", r.finishCode, r.finishErr, strings.Join(r.host.calls, "\n"))
		}
		assertNoStage(t, r)
	})
	t.Run("still running after stop", func(t *testing.T) {
		r := newLowSpaceRun(t)
		for i := 0; i < 32; i++ {
			r.host.running = append(r.host.running, true)
		}
		_, _ = r.run(r.env(nil), UpdateOptions{})
		if r.finishCode != UpdateExitFailed || r.finishErr == nil || !strings.Contains(r.finishErr.Error(), "still running") || readTarget(t, r.target) != "old core" {
			t.Fatalf("code %d err %v", r.finishCode, r.finishErr)
		}
	})
	t.Run("incomplete hand-over", func(t *testing.T) {
		host, target := newFakeUpdateHost(t, []byte("new core"), "1.14.3-lx.16")
		code, err := finishLowSpaceUpdate(host.env(target), LowSpaceFinishOptions{StageDir: t.TempDir(), Target: target})
		if code != UpdateExitFailed || err == nil || len(host.calls) != 0 || readTarget(t, target) != "old core" {
			t.Fatalf("code %d err %v calls %v", code, err, host.calls)
		}
	})
}
