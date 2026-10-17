//go:build with_lxd && unix

package lxd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// hookReloader is the fake service of the hook tests: it also answers the
// actual tun interface names, as the real service does from the box.
type hookReloader struct {
	fakeReloader
	names map[string]string
}

func (f *hookReloader) TunInterfaceNames(tags []string) []string {
	names := make([]string, len(tags))
	for index, tag := range tags {
		names[index] = f.names[tag]
	}
	return names
}

const (
	hookConfigTwoTuns = `{"inbounds": [
		{"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30", "fdfe:dcba:9876::1/126"], "auto_route": true},
		{"type": "mixed", "tag": "mixed-in", "listen_port": 2080},
		{"type": "tun", "address": "10.9.0.1/24", "mtu": 1400, "interface_name": "lx0"}
	]}`
	hookConfigNoTun = `{"inbounds": [{"type": "mixed", "listen_port": 2080}], "v": 2}`
)

// traceScript appends one record per run — the event and every LXD_* variable
// — to $LXD_HOOK_DIR/trace, so a test reads the whole sequence back.
const traceScript = "#!/bin/sh\n{ echo \"--- $LXD_EVENT\"; env | grep '^LXD_' | sort; } >> \"$LXD_HOOK_DIR/trace\"\nexit 0\n"

type hookRecord struct {
	event string
	env   map[string]string
}

func readTrace(t *testing.T, runner *hookRunner) []hookRecord {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(runner.dir, "trace"))
	if err != nil {
		return nil
	}
	var records []hookRecord
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if strings.HasPrefix(line, "--- ") {
			records = append(records, hookRecord{event: strings.TrimPrefix(line, "--- "), env: map[string]string{}})
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		records[len(records)-1].env[key] = value
	}
	return records
}

func expectSequence(t *testing.T, records []hookRecord, want ...string) {
	t.Helper()
	var got []string
	for _, record := range records {
		got = append(got, record.event+"("+record.env["LXD_REASON"]+")")
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("hook sequence: got %v, want %v", got, want)
	}
}

func expectEnv(t *testing.T, record hookRecord, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if record.env[key] != value {
			t.Errorf("%s: %s = %q, want %q", record.event, key, record.env[key], value)
		}
	}
}

// newHookTestController builds a controller over a fresh state dir with the
// hooks enabled and the owner check off (the tests do not run as root).
func newHookTestController(t *testing.T, service reloader, config *HooksConfig) *controller {
	t.Helper()
	stateDir := t.TempDir()
	stateStore, err := newStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if config == nil {
		config = &HooksConfig{Enabled: true, UnsafeSkipOwnerCheck: true}
	}
	runner, err := newHookRunner(config, stateDir, "", []string{"LXD_PLATFORM=test"}, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	return &controller{
		service:  service,
		store:    stateStore,
		validate: func(ctx context.Context, configPath string) error { return nil },
		hooks:    runner,
	}
}

func writeHook(t *testing.T, runner *hookRunner, event hookEvent, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runner.dir, string(event)), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeAllHooks(t *testing.T, runner *hookRunner, script string) {
	t.Helper()
	for _, event := range hookEvents {
		writeHook(t, runner, event, script)
	}
}

func seedLastGood(t *testing.T, control *controller, content string) {
	t.Helper()
	if err := control.store.SaveLastGood(content); err != nil {
		t.Fatal(err)
	}
}

func outcomeOf(control *controller, event hookEvent) hookOutcome {
	view := control.hooks.statusView()
	return view[string(event)].(hookOutcome)
}

func TestHooksConfigDefaults(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		config  *HooksConfig
		enabled bool
		timeout time.Duration
	}{
		{"no key", nil, false, 15 * time.Second},
		{"disabled", &HooksConfig{}, false, 15 * time.Second},
		{"enabled zero timeout", &HooksConfig{Enabled: true}, true, 15 * time.Second},
		{"enabled timeout", &HooksConfig{Enabled: true, TimeoutSeconds: 3}, true, 3 * time.Second},
		{"unsafe", &HooksConfig{Enabled: true, UnsafeSkipOwnerCheck: true}, true, 15 * time.Second},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stateDir := t.TempDir()
			stateStore, _ := newStore(stateDir)
			runner, err := newHookRunner(testCase.config, stateDir, "", nil, stateStore)
			if err != nil {
				t.Fatal(err)
			}
			if runner.enabled != testCase.enabled || runner.timeout != testCase.timeout {
				t.Fatalf("enabled %v timeout %v", runner.enabled, runner.timeout)
			}
			if runner.skipOwnerCheck != (testCase.config != nil && testCase.config.UnsafeSkipOwnerCheck) {
				t.Fatal("skipOwnerCheck not taken from the config")
			}
			if !runner.enabled {
				// Disabled: every event is a no-op, nothing is recorded.
				if err := runner.preStart(hookOrigin{reason: hookReasonBoot}, coreFacts{}); err != nil {
					t.Fatal(err)
				}
				if outcome := runner.statusView()["pre_start"].(hookOutcome); outcome.State != hookStateNever {
					t.Fatal("disabled hooks must not record outcomes")
				}
			}
		})
	}
	if err := validateHooks(&HooksConfig{TimeoutSeconds: -1}); err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Fatalf("negative timeout must refuse naming the key, got %v", err)
	}
	// Through daemon.json.
	stateDir := t.TempDir()
	if err := os.WriteFile(daemonConfigPath(stateDir), []byte(`{"hooks": {"enabled": true, "timeout_seconds": -5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDaemonConfig(stateDir); err == nil || !strings.Contains(err.Error(), "hooks") {
		t.Fatalf("daemon.json with a negative timeout must not load, got %v", err)
	}
	if err := os.WriteFile(daemonConfigPath(stateDir), []byte(`{"hooks": {"enabled": true, "timeout_seconds": 20, "unsafe_skip_owner_check": true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, found, err := LoadDaemonConfig(stateDir)
	if err != nil || !found || config.Hooks == nil || !config.Hooks.Enabled || config.Hooks.TimeoutSeconds != 20 || !config.Hooks.UnsafeSkipOwnerCheck {
		t.Fatalf("hooks key not loaded: %+v %v", config.Hooks, err)
	}
}

func TestHooksStubsCreatedNotOverwritten(t *testing.T) {
	stateDir := t.TempDir()
	stateStore, _ := newStore(stateDir)
	// An admin's file written before the first start must survive.
	hooksDir := filepath.Join(stateDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "#!/bin/sh\necho mine\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "post_stop"), []byte(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := newHookRunner(&HooksConfig{Enabled: true, UnsafeSkipOwnerCheck: true}, stateDir, "", nil, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(hooksDir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("hook dir mode: %v %v", info.Mode(), err)
	}
	for _, event := range hookEvents {
		path := filepath.Join(hooksDir, string(event))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s: mode %v", event, info.Mode())
		}
		if event == hookPostStop {
			if string(content) != custom {
				t.Fatal("an existing hook was overwritten")
			}
			continue
		}
		if string(content) != string(hookStub(event)) {
			t.Fatalf("%s: not the reference stub", event)
		}
		if !strings.Contains(string(content), "LXD_EVENT="+string(event)) || !strings.HasPrefix(string(content), "#!/bin/sh\n") {
			t.Fatalf("%s: stub lacks the shebang or its own event", event)
		}
	}
	// A stub is "not configured": skipped with a Warn, no error even for the gate.
	if err := runner.preStart(hookOrigin{reason: hookReasonBoot}, coreFacts{}); err != nil {
		t.Fatal("a stub pre_start must not block the start:", err)
	}
	outcome := runner.statusView()["pre_start"].(hookOutcome)
	if outcome.State != hookStateSkipped || outcome.Detail != "not configured (stub unchanged)" {
		t.Fatalf("stub must be skipped as not configured, got %+v", outcome)
	}
	if err := os.Remove(filepath.Join(hooksDir, "post_start")); err != nil {
		t.Fatal(err)
	}
	runner.postStart(hookOrigin{reason: hookReasonBoot}, coreFacts{}, "")
	if outcome := runner.statusView()["post_start"].(hookOutcome); outcome.State != hookStateSkipped || outcome.Detail != "not configured (no file)" {
		t.Fatalf("missing file must be skipped, got %+v", outcome)
	}
	// Log dir without a daemon log: a daemon-owned subdirectory of hooks/.
	if runner.logDir != filepath.Join(hooksDir, "log") {
		t.Fatal("log dir without log_file:", runner.logDir)
	}
	if hookLogDir("/tmp/lxd.log", hooksDir) != "/tmp/hooks" {
		t.Fatal("log dir beside the daemon log")
	}
}

func TestHooksEventSequenceBootApplyStop(t *testing.T) {
	service := &hookReloader{names: map[string]string{"tun-in": "utun7", "2": "lx0"}}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, hookConfigTwoTuns)

	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if !control.running {
		t.Fatal("boot failed")
	}
	// REST apply by a named client: a reload with _PREV from the two-tun core.
	ctx := withHookOrigin(context.Background(), hookReasonApply, "lxbox")
	if result := control.Apply(ctx, hookConfigNoTun); result.Outcome != applyApplied {
		t.Fatal("apply failed:", result.Err)
	}
	if err := control.stopFrom(hookOrigin{reason: hookReasonStop, client: "lxbox"}); err != nil {
		t.Fatal(err)
	}

	records := readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_start(boot)", "post_reload(apply)", "post_stop(stop)")
	twoTunsSHA := contentSHA(hookConfigTwoTuns)
	noTunSHA := contentSHA(hookConfigNoTun)
	expectEnv(t, records[0], map[string]string{
		"LXD_CLIENT":              "",
		"LXD_CONFIG_SHA":          twoTunsSHA,
		"LXD_CONFIG_SHA_PREV":     "",
		"LXD_TUN_ADDRESS":         "172.19.0.1/30,fdfe:dcba:9876::1/126,10.9.0.1/24",
		"LXD_TUN_INTERFACE":       ",lx0", // pre_start: explicit names only
		"LXD_TUN_MTU":             "9000,1400",
		"LXD_TUN_AUTO_ROUTE":      "1,0",
		"LXD_TUN_ADDRESS_PREV":    "",
		"LXD_HOOK_DIR":            control.hooks.dir,
		"LXD_HOOK_TIMEOUT":        "15",
		"LXD_PLATFORM":            "test",
		"LXD_LOG_FILE":            "",
		"LXD_ERROR":               "",
		"LXD_TUN_AUTO_ROUTE_PREV": "",
	})
	expectEnv(t, records[1], map[string]string{
		"LXD_CONFIG_SHA":    twoTunsSHA,
		"LXD_TUN_INTERFACE": "utun7,lx0", // post_start: actual names from the box
		"LXD_TUN_ADDRESS":   "172.19.0.1/30,fdfe:dcba:9876::1/126,10.9.0.1/24",
	})
	expectEnv(t, records[2], map[string]string{
		"LXD_CLIENT":              "lxbox",
		"LXD_CONFIG_SHA":          noTunSHA,
		"LXD_CONFIG_SHA_PREV":     twoTunsSHA,
		"LXD_TUN_ADDRESS":         "",
		"LXD_TUN_INTERFACE":       "",
		"LXD_TUN_MTU":             "",
		"LXD_TUN_AUTO_ROUTE":      "",
		"LXD_TUN_ADDRESS_PREV":    "172.19.0.1/30,fdfe:dcba:9876::1/126,10.9.0.1/24",
		"LXD_TUN_INTERFACE_PREV":  "utun7,lx0",
		"LXD_TUN_MTU_PREV":        "9000,1400",
		"LXD_TUN_AUTO_ROUTE_PREV": "1,0",
		"LXD_ERROR":               "",
	})
	expectEnv(t, records[3], map[string]string{
		"LXD_CLIENT":           "lxbox",
		"LXD_CONFIG_SHA":       "",
		"LXD_CONFIG_SHA_PREV":  noTunSHA,
		"LXD_TUN_ADDRESS_PREV": "",
	})
	for _, event := range hookEvents {
		if outcome := outcomeOf(control, event); outcome.State != hookStateOK || outcome.ExitCode == nil || *outcome.ExitCode != 0 || outcome.FinishedAt == "" {
			t.Fatalf("%s outcome %+v", event, outcome)
		}
	}
}

func TestHooksSighupAndManualRollbackReasons(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, `{"v": 1}`)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if result := control.Apply(withHookOrigin(context.Background(), hookReasonSighup, ""), `{"v": 2}`); result.Outcome != applyApplied {
		t.Fatal(result.Err)
	}
	// POST /admin/rollback with a live core: a reload, reason rollback, no error.
	if result, found := control.Rollback(withHookOrigin(context.Background(), hookReasonRollback, "op")); !found || result.Outcome != applyApplied {
		t.Fatal("rollback failed")
	}
	// POST /admin/start over a stopped core: a start, reason start.
	if err := control.Stop(); err != nil {
		t.Fatal(err)
	}
	if result, found := control.Start(withHookOrigin(context.Background(), hookReasonStart, "op")); !found || result.Outcome != applyApplied {
		t.Fatal("start failed")
	}
	records := readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_start(boot)", "post_reload(sighup)", "post_reload(rollback)", "post_stop(stop)", "pre_start(start)", "post_start(start)")
	expectEnv(t, records[2], map[string]string{"LXD_CLIENT": ""})
	expectEnv(t, records[3], map[string]string{"LXD_CLIENT": "op", "LXD_ERROR": "", "LXD_CONFIG_SHA": contentSHA(`{"v": 2}`)})
	expectEnv(t, records[6], map[string]string{"LXD_CLIENT": "op", "LXD_CONFIG_SHA": contentSHA(`{"v": 2}`)})
}

func TestHooksPreStartFailClosed(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, nil)
	writeHook(t, control.hooks, hookPreStart, "#!/bin/sh\necho uplink is down >&2\nexit 1\n")
	seedLastGood(t, control, `{"v": 1}`)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if len(service.calls) != 0 {
		t.Fatal("a failed pre_start must not start the core")
	}
	recorder := httptest.NewRecorder()
	control.handleStatus(recorder, httptest.NewRequest(http.MethodGet, "/admin/status", nil))
	var status struct {
		Status    string `json:"status"`
		LastError string `json:"last_error"`
		Hooks     struct {
			Enabled  bool        `json:"enabled"`
			PreStart hookOutcome `json:"pre_start"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "fatal" || !strings.Contains(status.LastError, "hook pre_start") || !strings.Contains(status.LastError, "uplink is down") {
		t.Fatalf("status %+v", status)
	}
	if !status.Hooks.Enabled || status.Hooks.PreStart.State != hookStateFailed || *status.Hooks.PreStart.ExitCode != 1 || status.Hooks.PreStart.Reason != hookReasonBoot {
		t.Fatalf("hooks status %+v", status.Hooks)
	}
	// Apply over no core is gated too.
	if result := control.Apply(context.Background(), `{"v": 2}`); result.Outcome != applyFailed || len(service.calls) != 0 {
		t.Fatalf("apply must be cancelled by pre_start: %+v calls %v", result, service.calls)
	}
}

func TestHooksPostStartFailureKeepsCore(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, nil)
	writeHook(t, control.hooks, hookPostStart, "#!/bin/sh\necho networksetup: no such service\nexit 2\n")
	writeHook(t, control.hooks, hookPostStop, "#!/bin/sh\nexit 1\n")
	seedLastGood(t, control, `{"v": 1}`)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if !control.running {
		t.Fatal("the core must keep running after a failed post_start")
	}
	outcome := outcomeOf(control, hookPostStart)
	if outcome.State != hookStateFailed || outcome.ExitCode == nil || *outcome.ExitCode != 2 || !strings.Contains(outcome.Detail, "networksetup") {
		t.Fatalf("post_start outcome %+v", outcome)
	}
	if err := control.Stop(); err != nil {
		t.Fatal("a failed post_stop must not fail the stop:", err)
	}
	if control.running {
		t.Fatal("stop did not go through")
	}
	if outcome := outcomeOf(control, hookPostStop); outcome.State != hookStateFailed || *outcome.ExitCode != 1 {
		t.Fatalf("post_stop outcome %+v", outcome)
	}
}

func TestHooksTimeoutKillsProcessGroup(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, &HooksConfig{Enabled: true, TimeoutSeconds: 1, UnsafeSkipOwnerCheck: true})
	writeHook(t, control.hooks, hookPostStart, "#!/bin/sh\necho $$ > \"$LXD_HOOK_DIR/pid\"\nsleep 60\n")
	seedLastGood(t, control, `{"v": 1}`)
	startedAt := time.Now()
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if elapsed := time.Since(startedAt); elapsed > 4*time.Second {
		t.Fatal("the daemon waited past the timeout:", elapsed)
	}
	outcome := outcomeOf(control, hookPostStart)
	if outcome.State != hookStateTimeout || !strings.Contains(outcome.Detail, "timed out after 1s") {
		t.Fatalf("outcome %+v", outcome)
	}
	raw, err := os.ReadFile(filepath.Join(control.hooks.dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if pidAlive(pid) {
		t.Fatal("the hook's shell is still alive after the timeout")
	}
	if !control.running {
		t.Fatal("a timed-out post_start must not stop the core")
	}
}

func TestHooksFatalAfterPreStart(t *testing.T) {
	service := &hookReloader{fakeReloader: fakeReloader{failFor: map[string]error{hookConfigTwoTuns: E.New("configure tun interface: busy")}}}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, hookConfigTwoTuns)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	records := readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_stop(fatal)")
	expectEnv(t, records[1], map[string]string{
		"LXD_ERROR":              "configure tun interface: busy",
		"LXD_CONFIG_SHA_PREV":    contentSHA(hookConfigTwoTuns),
		"LXD_TUN_ADDRESS_PREV":   "172.19.0.1/30,fdfe:dcba:9876::1/126,10.9.0.1/24",
		"LXD_TUN_INTERFACE_PREV": ",lx0", // fatal: explicit names only
		"LXD_CONFIG_SHA":         "",
	})
	if _, err := os.Stat(control.hooks.statePath()); !os.IsNotExist(err) {
		t.Fatal("no state.json after a fatal start")
	}
}

func TestHooksRollbackReasons(t *testing.T) {
	service := &hookReloader{fakeReloader: fakeReloader{failFor: map[string]error{}}, names: map[string]string{"tun-in": "utun3", "2": "lx0"}}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, hookConfigTwoTuns)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})

	service.failFor[hookConfigNoTun] = E.New("bind failed")
	result := control.Apply(withHookOrigin(context.Background(), hookReasonApply, "lxbox"), hookConfigNoTun)
	if result.Outcome != applyFailed || !result.RolledBack {
		t.Fatalf("expected rollback, got %+v", result)
	}
	records := readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_start(boot)", "post_reload(rollback)")
	expectEnv(t, records[2], map[string]string{
		"LXD_CLIENT":             "lxbox",
		"LXD_ERROR":              "bind failed",
		"LXD_CONFIG_SHA":         contentSHA(hookConfigTwoTuns), // last-good is current again
		"LXD_CONFIG_SHA_PREV":    contentSHA(hookConfigTwoTuns),
		"LXD_TUN_INTERFACE":      "utun3,lx0",
		"LXD_TUN_INTERFACE_PREV": "utun3,lx0",
	})

	// Last-good fails too: nothing remains — post_stop(fatal) with the facts
	// of the instance that was live before the swap.
	service.failFor[hookConfigTwoTuns] = E.New("rollback bind failed")
	result = control.Apply(context.Background(), hookConfigNoTun)
	if result.Outcome != applyFailed || result.RolledBack {
		t.Fatalf("expected fatal, got %+v", result)
	}
	records = readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_start(boot)", "post_reload(rollback)", "post_stop(fatal)")
	expectEnv(t, records[3], map[string]string{
		"LXD_CONFIG_SHA_PREV":    contentSHA(hookConfigTwoTuns),
		"LXD_TUN_INTERFACE_PREV": "utun3,lx0",
	})
	if !strings.Contains(records[3].env["LXD_ERROR"], "bind failed") || !strings.Contains(records[3].env["LXD_ERROR"], "rollback also failed") {
		t.Fatal("LXD_ERROR must carry both failures:", records[3].env["LXD_ERROR"])
	}
}

func TestHooksStateJSONAndRecover(t *testing.T) {
	service := &hookReloader{names: map[string]string{"tun-in": "utun5", "2": "lx0"}}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, hookConfigTwoTuns)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})

	raw, err := os.ReadFile(control.hooks.statePath())
	if err != nil {
		t.Fatal("state.json must exist after post_start:", err)
	}
	var state hookState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.ConfigSHA != contentSHA(hookConfigTwoTuns) || state.TunInterface != "utun5,lx0" || state.TunAddress == "" || state.PID != os.Getpid() || state.StartedAt == "" {
		t.Fatalf("state.json %+v", state)
	}
	if err := control.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(control.hooks.statePath()); !os.IsNotExist(err) {
		t.Fatal("state.json must be gone after post_stop")
	}

	// Simulate a crash: the record is there, the process is gone. A new
	// controller over the same state dir recovers before pre_start.
	control.hooks.writeState(coreFacts{ConfigSHA: "deadbeef", TunAddress: "172.19.0.1/30", TunInterface: "utun5", TunMTU: "9000", TunAutoRoute: "1"})
	_ = control.store.SetWasRunning(true) // the crashed daemon was running
	os.Remove(filepath.Join(control.hooks.dir, "trace"))
	runner, err := newHookRunner(&HooksConfig{Enabled: true, UnsafeSkipOwnerCheck: true}, control.store.dir, "", nil, control.store)
	if err != nil {
		t.Fatal(err)
	}
	fresh := &controller{service: service, store: control.store, validate: control.validate, hooks: runner}
	bootstrap(context.Background(), fresh, fresh.store, Options{StateDir: fresh.store.dir})
	records := readTrace(t, runner)
	expectSequence(t, records, "post_stop(recover)", "pre_start(boot)", "post_start(boot)")
	expectEnv(t, records[0], map[string]string{
		"LXD_CONFIG_SHA_PREV":     "deadbeef",
		"LXD_TUN_ADDRESS_PREV":    "172.19.0.1/30",
		"LXD_TUN_INTERFACE_PREV":  "utun5",
		"LXD_TUN_MTU_PREV":        "9000",
		"LXD_TUN_AUTO_ROUTE_PREV": "1",
		"LXD_CONFIG_SHA":          "",
	})
	// The new start wrote its own record.
	raw, err = os.ReadFile(runner.statePath())
	if err != nil || strings.Contains(string(raw), "deadbeef") {
		t.Fatal("state.json must describe the new core:", string(raw), err)
	}

	// Disabled hooks: a leftover record is removed silently, no recover.
	control.hooks.writeState(coreFacts{ConfigSHA: "stale"})
	disabled, _ := newHookRunner(&HooksConfig{}, control.store.dir, "", nil, control.store)
	disabled.recoverAtBoot()
	if _, err := os.Stat(disabled.statePath()); !os.IsNotExist(err) {
		t.Fatal("a disabled daemon must drop a stale state.json")
	}
}

func TestHooksOutputFileNotPipe(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, &HooksConfig{Enabled: true, TimeoutSeconds: 10, UnsafeSkipOwnerCheck: true})
	writeHook(t, control.hooks, hookPostStart, "#!/bin/sh\necho hi\n(sleep 30 &)\nexit 0\n")
	seedLastGood(t, control, `{"v": 1}`)
	startedAt := time.Now()
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if elapsed := time.Since(startedAt); elapsed > 5*time.Second {
		t.Fatal("the daemon waited for the background child:", elapsed)
	}
	if outcome := outcomeOf(control, hookPostStart); outcome.State != hookStateOK {
		t.Fatalf("outcome %+v", outcome)
	}
	logContent, err := os.ReadFile(filepath.Join(control.hooks.logDir, "post_start.log"))
	if err != nil || strings.TrimSpace(string(logContent)) != "hi" {
		t.Fatalf("post_start.log = %q, %v", logContent, err)
	}
}

// rootedLstat pretends every component outside the hook dir is root 0755
// and that the hook dir and its files are root-owned with their real modes:
// the chain check then exercises exactly the modes the test sets, on any
// host, as a non-root user.
func rootedLstat(hookDir string) func(string) (ownerInfo, error) {
	return func(path string) (ownerInfo, error) {
		if !strings.HasPrefix(path, hookDir) {
			return ownerInfo{uid: 0, mode: os.ModeDir | 0o755}, nil
		}
		info, err := lstatOwner(path)
		if err != nil {
			return ownerInfo{}, err
		}
		info.uid = 0
		return info, nil
	}
}

func TestHooksOwnershipGate(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, &HooksConfig{Enabled: true})
	runner := control.hooks
	runner.lstat = rootedLstat(runner.dir)
	writeHook(t, runner, hookPreStart, "#!/bin/sh\nexit 0\n")
	seedLastGood(t, control, `{"v": 1}`)
	path := filepath.Join(runner.dir, "pre_start")

	// A sane file passes and the core starts.
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	if !control.running {
		t.Fatal("a root-owned 0755 hook must pass the gate")
	}
	if err := control.Stop(); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name    string
		arrange func()
		want    string
	}{
		{"world-writable file", func() { os.Chmod(path, 0o777) }, "not group/world-writable"},
		{"group-writable dir", func() { os.Chmod(path, 0o755); os.Chmod(runner.dir, 0o775) }, "not group/world-writable"},
		{"symlink", func() {
			os.Chmod(runner.dir, 0o755)
			os.Remove(path)
			target := filepath.Join(t.TempDir(), "real_hook")
			os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755)
			os.Symlink(target, path)
		}, "symbolic link"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.arrange()
			calls := len(service.calls)
			result, found := control.Start(context.Background())
			if !found || result.Outcome != applyFailed || len(service.calls) != calls {
				t.Fatalf("the start must be cancelled by the gate: %+v", result)
			}
			outcome := outcomeOf(control, hookPreStart)
			if outcome.State != hookStateFailed || !strings.Contains(outcome.Detail, testCase.want) {
				t.Fatalf("outcome %+v, want %q", outcome, testCase.want)
			}
		})
	}
	// With the check off the symlinked hook runs.
	runner.skipOwnerCheck = true
	if result, _ := control.Start(context.Background()); result.Outcome != applyApplied {
		t.Fatal("unsafe_skip_owner_check must let the hook run:", result.Err)
	}
	if outcome := outcomeOf(control, hookPreStart); outcome.State != hookStateOK {
		t.Fatalf("outcome %+v", outcome)
	}
}

func TestHooksFlockSerializesProcesses(t *testing.T) {
	stateDir := t.TempDir()
	stateStore, _ := newStore(stateDir)
	config := &HooksConfig{Enabled: true, TimeoutSeconds: 10, UnsafeSkipOwnerCheck: true}
	first, err := newHookRunner(config, stateDir, "", nil, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newHookRunner(config, stateDir, "", nil, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	writeHook(t, first, hookPostStart, "#!/bin/sh\necho start >> \"$LXD_HOOK_DIR/seq\"\nsleep 0.3\necho end >> \"$LXD_HOOK_DIR/seq\"\n")
	writeHook(t, first, hookPostStop, "#!/bin/sh\necho start >> \"$LXD_HOOK_DIR/seq\"\nsleep 0.3\necho end >> \"$LXD_HOOK_DIR/seq\"\n")
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); first.postStart(hookOrigin{reason: hookReasonBoot}, coreFacts{}, "") }()
	go func() { defer wait.Done(); second.postStop(hookOrigin{reason: hookReasonShutdown}, coreFacts{}, "") }()
	wait.Wait()
	sequence, err := os.ReadFile(filepath.Join(stateDir, "hooks", "seq"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(sequence)) != "start\nend\nstart\nend" {
		t.Fatalf("hooks ran concurrently: %q", sequence)
	}
}

func TestHooksShutdownAnnouncesPostStop(t *testing.T) {
	service := &hookReloader{}
	control := newHookTestController(t, service, nil)
	writeAllHooks(t, control.hooks, traceScript)
	seedLastGood(t, control, `{"v": 1}`)
	bootstrap(context.Background(), control, control.store, Options{StateDir: control.store.dir})
	// The shutdown path of Run: facts before the close, the hook after.
	previous, wasRunning := control.liveFacts()
	_ = control.service.CloseService()
	if wasRunning {
		control.hooks.postStop(hookOrigin{reason: hookReasonShutdown}, previous, "")
	}
	records := readTrace(t, control.hooks)
	expectSequence(t, records, "pre_start(boot)", "post_start(boot)", "post_stop(shutdown)")
	expectEnv(t, records[2], map[string]string{"LXD_CONFIG_SHA_PREV": contentSHA(`{"v": 1}`), "LXD_CLIENT": ""})
}

func TestHooksConfigFactsParsing(t *testing.T) {
	facts := configFacts("// comment\n{\"inbounds\": [{\"type\": \"tun\", \"address\": \"10.0.0.1/24\"},]}", nil)
	if facts.TunAddress != "10.0.0.1/24" || facts.TunMTU != "9000" || facts.TunAutoRoute != "0" || facts.TunInterface != "" {
		t.Fatalf("facts %+v", facts)
	}
	if facts := configFacts(`{"broken`, nil); facts.TunAddress != "" || facts.ConfigSHA == "" {
		t.Fatalf("unparsable config: %+v", facts)
	}
	if facts := configFacts(`{"inbounds": []}`, nil); facts.TunAddress != "" || facts.TunMTU != "" {
		t.Fatalf("no tun: %+v", facts)
	}
}
