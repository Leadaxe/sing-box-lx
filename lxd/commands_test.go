//go:build with_lxd && unix

package lxd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestValidateCommands(t *testing.T) {
	good := map[string]string{"update": "/usr/bin/sing-box lxd update", "a": "x", "re-start_2": "true"}
	if err := validateCommands(good); err != nil {
		t.Fatal(err)
	}
	if err := validateCommands(nil); err != nil {
		t.Fatal("no commands must validate", err)
	}
	for _, bad := range []map[string]string{
		{"Update": "x"},
		{"-x": "x"},
		{"a b": "x"},
		{strings.Repeat("a", 33): "x"},
		{"": "x"},
		{"update": ""},
	} {
		if err := validateCommands(bad); err == nil {
			t.Fatalf("%v must be rejected", bad)
		}
	}
}

// commandsController wires a runner with real /bin/sh over a temp outcome dir.
func commandsController(t *testing.T, commands map[string]string) (*controller, http.Handler) {
	t.Helper()
	control := newTestController(t, &fakeReloader{}, nil)
	control.platform = platformInfo{goos: "linux", goarch: "arm64", platform: "linux-arm64", initSystem: "procd", service: true}
	control.infoStateDir = t.TempDir()
	runner, err := newCommandRunner(commands, filepath.Join(t.TempDir(), "commands"),
		commandEnv(control.platform, control.infoStateDir, "/usr/bin/sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	control.commands = runner
	return control, control.adminHandler("")
}

// waitDone polls GET until the command has an exit code.
func waitDone(t *testing.T, handler http.Handler, name string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, payload := serveAdmin(t, handler, httptest.NewRequest(http.MethodGet, "/admin/commands/"+name, nil))
		if status != http.StatusOK {
			t.Fatal("GET must be served, got", status)
		}
		if payload["state"] == commandDone {
			return payload
		}
		if time.Now().After(deadline) {
			t.Fatalf("command never finished: %v", payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCommandRunRecordsOutcome(t *testing.T) {
	_, handler := commandsController(t, map[string]string{"hi": "echo hi; echo oops >&2; exit 3"})

	status, payload := serveAdmin(t, handler, httptest.NewRequest(http.MethodGet, "/admin/commands", nil))
	if status != http.StatusOK {
		t.Fatal("list must be served, got", status)
	}
	list, _ := payload["commands"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["state"] != commandIdle || list[0].(map[string]any)["command"] != "echo hi; echo oops >&2; exit 3" {
		t.Fatalf("list = %v", payload)
	}

	status, payload = serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/hi", nil))
	if status != http.StatusAccepted || payload["client"] != "operator" || payload["started_at"] == "" {
		t.Fatalf("POST = %d %v", status, payload)
	}
	done := waitDone(t, handler, "hi")
	if code, _ := done["exit_code"].(float64); int(code) != 3 || done["log"] != "hi\noops\n" || done["finished_at"] == "" {
		t.Fatalf("done = %v", done)
	}
}

func TestCommandEnvironment(t *testing.T) {
	control, handler := commandsController(t, map[string]string{"env": "env"})
	if status, _ := serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/env", nil)); status != http.StatusAccepted {
		t.Fatal("POST = ", status)
	}
	logText, _ := waitDone(t, handler, "env")["log"].(string)
	for _, want := range []string{
		"LXD_PLATFORM=linux-arm64\n", "LXD_INIT_SYSTEM=procd\n", "LXD_CLIENT=operator\n",
		"LXD_STATE_DIR=" + control.infoStateDir + "\n", "LXD_BIN=/usr/bin/sing-box\n", "LXD_VERSION=",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("environment lacks %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "LXD_CMD=") {
		t.Fatal("the wrapper's own variables must not leak into the command")
	}
}

func TestCommandConflictAndDetached(t *testing.T) {
	control, handler := commandsController(t, map[string]string{"slow": "sleep 1"})
	status, payload := serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/slow", nil))
	if status != http.StatusAccepted || payload["state"] != commandRunning {
		t.Fatalf("POST = %d %v", status, payload)
	}
	// Second POST while running: 409 with the running view, no second process.
	if status, payload = serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/slow", nil)); status != http.StatusConflict || payload["state"] != commandRunning {
		t.Fatalf("second POST = %d %v", status, payload)
	}
	// Detached: the wrapper leads its own session, so killing the daemon's
	// process group would not reach it.
	var run commandRun
	readJSONFile(t, control.commands.runPath("slow"), &run)
	if sid, err := unix.Getsid(run.PID); err != nil || sid != run.PID {
		t.Fatalf("wrapper pid %d sid %d err %v: must lead its own session", run.PID, sid, err)
	}
	// A fresh runner over the same directory (the daemon restarted) reads the
	// state from the files.
	again, err := newCommandRunner(control.commands.commands, control.commands.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view := again.view("slow", false); view.State != commandRunning {
		t.Fatalf("after restart state = %s", view.State)
	}
	waitDone(t, handler, "slow")
	// Lost: a run record without an exit code whose pid is gone.
	_ = os.Remove(control.commands.exitPath("slow"))
	again.alive = func(int) bool { return false }
	if view := again.view("slow", false); view.State != commandLost {
		t.Fatalf("dead pid without exit must be lost, got %s", view.State)
	}
}

func TestCommandRoutesAndInput(t *testing.T) {
	_, handler := commandsController(t, map[string]string{"ok": "true"})
	if status, payload := serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/nope", nil)); status != http.StatusNotFound {
		t.Fatalf("unknown name = %d %v", status, payload)
	}
	if status, _ := serveAdmin(t, handler, httptest.NewRequest(http.MethodGet, "/admin/commands/nope", nil)); status != http.StatusNotFound {
		t.Fatal("unknown name GET = ", status)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/commands/ok", strings.NewReader(`{"arg":"x"}`))
	if status, payload := serveAdmin(t, handler, request); status != http.StatusBadRequest || payload["error"] != "commands take no input" {
		t.Fatalf("body must be refused, got %d %v", status, payload)
	}

	// No commands configured: the routes do not exist.
	bare := newTestController(t, &fakeReloader{}, nil).adminHandler("")
	for _, probe := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/admin/commands", nil),
		httptest.NewRequest(http.MethodGet, "/admin/commands/ok", nil),
		httptest.NewRequest(http.MethodPost, "/admin/commands/ok", nil),
	} {
		recorder := httptest.NewRecorder()
		bare.ServeHTTP(recorder, probe)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s without commands = %d", probe.Method, probe.URL.Path, recorder.Code)
		}
	}
}

func TestCommandOutcomeDirAndCleanup(t *testing.T) {
	if got := commandOutcomeDir("/tmp/lxd.log", "/etc/sing-box-lxd/state"); got != "/tmp/commands" {
		t.Fatalf("beside the log: %s", got)
	}
	if got := commandOutcomeDir("", "/etc/sing-box-lxd/state"); got != "/etc/sing-box-lxd/state/commands" {
		t.Fatalf("without a log: %s", got)
	}
	dir := t.TempDir()
	for _, name := range []string{"old.json", "old.log", "old.exit", "keep.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newCommandRunner(map[string]string{"keep": "true"}, dir, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "keep.json" {
		t.Fatalf("outcomes of unconfigured names must go, left %v", entries)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatal(err)
	}
}

// TestMarkInheritableCloseOnExec: a descriptor opened without CLOEXEC (the
// way sing-tun opens /dev/net/tun) gets the flag; stdio is left alone.
func TestMarkInheritableCloseOnExec(t *testing.T) {
	var pipe [2]int
	if err := unix.Pipe(pipe[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	defer unix.Close(pipe[1])
	cloexec := func(fd int) bool {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		return flags&unix.FD_CLOEXEC != 0
	}
	if cloexec(pipe[0]) {
		t.Skip("unix.Pipe returned a close-on-exec descriptor; nothing to prove")
	}
	stdioBefore := []bool{cloexec(0), cloexec(1), cloexec(2)}
	markInheritableCloseOnExec()
	if !cloexec(pipe[0]) || !cloexec(pipe[1]) {
		t.Fatal("inheritable descriptors must be marked close-on-exec")
	}
	for fd, before := range stdioBefore {
		if cloexec(fd) != before {
			t.Fatalf("fd %d must not be touched", fd)
		}
	}
}

// TestCommandDoesNotInheritDescriptors: the spawned command must not see a
// descriptor the daemon holds without CLOEXEC — a copy of the TUN fd in a
// detached `lxd update` kept the interface alive across the service restart.
func TestCommandDoesNotInheritDescriptors(t *testing.T) {
	var pipe [2]int
	if err := unix.Pipe(pipe[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	defer unix.Close(pipe[1])
	probe := "if [ -e /dev/fd/" + strconv.Itoa(pipe[1]) + " ]; then echo leaked; else echo clean; fi"
	_, handler := commandsController(t, map[string]string{"fds": probe})
	if status, _ := serveAdmin(t, handler, httptest.NewRequest(http.MethodPost, "/admin/commands/fds", nil)); status != http.StatusAccepted {
		t.Fatal("POST = ", status)
	}
	if logText, _ := waitDone(t, handler, "fds")["log"].(string); logText != "clean\n" {
		t.Fatalf("the command inherited the daemon's descriptor: %q", logText)
	}
}
