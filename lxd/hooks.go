//go:build with_lxd

package lxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// Lifecycle hooks (SPEC 135): the admin puts four executables into
// <state_dir>/hooks/ — pre_start, post_start, post_reload, post_stop — and the
// daemon runs them at the boundaries of the core's life inside its own
// process, with facts about the core in the environment (LXD_EVENT,
// LXD_REASON, LXD_TUN_ADDRESS, LXD_TUN_INTERFACE, …). The daemon promises one
// thing only: every boundary is announced, including after a process crash
// (post_stop with reason `recover`, from hooks/core_snapshot.json) and across a
// service restart (the hook runs under an inter-process flock). What to do at
// the boundary — pointing the system DNS at the tun and restoring it — is the
// script's business; the daemon keeps no snapshot of the system (issue #12).

// HooksConfig is daemon.json's `hooks` object.
type HooksConfig struct {
	// Enabled turns the hooks on; absent key or false = no hooks (the
	// directory and the stubs are still created, so the admin sees where to
	// write).
	Enabled bool `json:"enabled,omitempty"`
	// TimeoutSeconds is the budget of one hook; 0/absent = 15, negative
	// refuses the daemon start.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// UnsafeSkipOwnerCheck disables the root-owned gate before every run —
	// for a dev run from a home directory; WARN on every daemon start.
	UnsafeSkipOwnerCheck bool `json:"unsafe_skip_owner_check,omitempty"`
}

const defaultHookTimeout = 15 * time.Second

// validateHooks applies the daemon.json rules: a negative timeout refuses the
// start, naming the key.
func validateHooks(config *HooksConfig) error {
	if config != nil && config.TimeoutSeconds < 0 {
		return E.New("daemon.json: hooks: timeout_seconds must not be negative, got ", config.TimeoutSeconds)
	}
	return nil
}

func (c *HooksConfig) timeout() time.Duration {
	if c == nil || c.TimeoutSeconds <= 0 {
		return defaultHookTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type hookEvent string

const (
	hookPreStart   hookEvent = "pre_start"
	hookPostStart  hookEvent = "post_start"
	hookPostReload hookEvent = "post_reload"
	hookPostStop   hookEvent = "post_stop"
)

var hookEvents = []hookEvent{hookPreStart, hookPostStart, hookPostReload, hookPostStop}

// Transition reasons (SPEC 135 §2.2), the value of LXD_REASON.
const (
	hookReasonBoot     = "boot"
	hookReasonStart    = "start"
	hookReasonApply    = "apply"
	hookReasonSighup   = "sighup"
	hookReasonRollback = "rollback"
	hookReasonStop     = "stop"
	hookReasonShutdown = "shutdown"
	hookReasonFatal    = "fatal"
	hookReasonRecover  = "recover"
)

// States of the last run of one event, as /admin/status reports them.
const (
	hookStateNever   = "never"
	hookStateOK      = "ok"
	hookStateFailed  = "failed"
	hookStateTimeout = "timeout"
	hookStateSkipped = "skipped"
)

// hookOutcome is the last outcome of one event (SPEC 135 §2.6).
type hookOutcome struct {
	State      string `json:"state"`
	Reason     string `json:"reason"`
	ExitCode   *int   `json:"exit_code"`
	FinishedAt string `json:"finished_at"`
	Detail     string `json:"detail"`
}

// coreFacts are the facts about one core the hooks receive: the current ones
// without a suffix, the previous ones with _PREV. Lists are already joined
// with commas, in config order; "" = unknown or not applicable.
type coreFacts struct {
	ConfigSHA    string `json:"config_sha"`
	TunAddress   string `json:"tun_address"`
	TunInterface string `json:"tun_interface"`
	TunMTU       string `json:"tun_mtu"`
	TunAutoRoute string `json:"tun_auto_route"`
}

// coreSnapshot is hooks/core_snapshot.json: the facts of the last live core, written by
// the daemon after every successful start and removed after post_stop. Its
// presence at boot means the previous process never reached post_stop.
type coreSnapshot struct {
	coreFacts
	StartedAt string `json:"started_at"`
	PID       int    `json:"pid"`
}

// hookOrigin says who caused a transition: the reason for LXD_REASON and the
// client name for LXD_CLIENT (a REST request), carried in the request context
// so the Apply/Rollback/Stop signatures stay as they are.
type hookOrigin struct {
	reason string
	client string
}

type hookOriginKey struct{}

func withHookOrigin(ctx context.Context, reason, client string) context.Context {
	return context.WithValue(ctx, hookOriginKey{}, hookOrigin{reason: reason, client: client})
}

// hookOriginFrom reads the origin; a context without one is an apply with no
// client (the unit tests' path).
func hookOriginFrom(ctx context.Context) hookOrigin {
	if origin, ok := ctx.Value(hookOriginKey{}).(hookOrigin); ok {
		return origin
	}
	return hookOrigin{reason: hookReasonApply}
}

// hookLogTail is how much of a failed hook's output the Error line carries.
const hookLogTail = 4 << 10

// hookRunner owns the hook directory and runs the events. nil-safe: a daemon
// without hooks (Windows, unit tests) has no runner and every call is a no-op.
type hookRunner struct {
	dir            string // <state_dir>/hooks — LXD_HOOK_DIR
	logDir         string // where <event>.log lands
	enabled        bool
	timeout        time.Duration
	skipOwnerCheck bool
	baseEnv        []string
	store          *store
	lstat          func(string) (ownerInfo, error)
	// run executes one hook: exit code, whether it timed out, or an error
	// when the process could not be started at all. The platform provides it
	// (hooks_unix.go); tests may swap it.
	run func(path string, env []string, logPath string, timeout time.Duration) (exitCode int, timedOut bool, err error)

	access   sync.Mutex
	outcomes map[hookEvent]*hookOutcome
}

// newHookRunner prepares <state_dir>/hooks: the directory 0755, a stub for
// every missing event 0755, the log directory. Done whether or not the hooks
// are enabled, so the admin sees where to write; with enabled=false the
// runner then does nothing but log one Info line here.
func newHookRunner(config *HooksConfig, stateDir, logFile string, baseEnv []string, stateStore *store) (*hookRunner, error) {
	dir := filepath.Join(stateDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, E.Cause(err, "create hook directory ", dir)
	}
	// Explicit: the umask may have trimmed the mode of a fresh directory.
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, E.Cause(err, "chmod hook directory ", dir)
	}
	for _, event := range hookEvents {
		path := filepath.Join(dir, string(event))
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, E.Cause(err, "stat hook ", path)
		}
		if err := os.WriteFile(path, hookStub(event), 0o755); err != nil {
			return nil, E.Cause(err, "write hook stub ", path)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return nil, E.Cause(err, "chmod hook stub ", path)
		}
	}
	logDir := hookLogDir(logFile, dir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, E.Cause(err, "create hook log directory ", logDir)
	}
	runner := &hookRunner{
		dir:            dir,
		logDir:         logDir,
		enabled:        config != nil && config.Enabled,
		timeout:        config.timeout(),
		skipOwnerCheck: config != nil && config.UnsafeSkipOwnerCheck,
		baseEnv: append(append([]string{}, baseEnv...),
			"LXD_LOG_FILE="+logFile,
			"LXD_HOOK_DIR="+dir,
			"LXD_HOOK_TIMEOUT="+strconv.Itoa(int(config.timeout()/time.Second)),
		),
		store:    stateStore,
		lstat:    lstatOwner,
		run:      runHookProcess,
		outcomes: make(map[hookEvent]*hookOutcome, len(hookEvents)),
	}
	for _, event := range hookEvents {
		runner.outcomes[event] = &hookOutcome{State: hookStateNever}
	}
	if !runner.enabled {
		log.Info("lxd: hooks disabled")
		return runner, nil
	}
	if runner.skipOwnerCheck {
		log.Warn("lxd: hooks: unsafe_skip_owner_check is on — the root-owned check of ", dir, " is off; anyone who can write a hook there runs code as root")
	}
	return runner, nil
}

// hookLogDir: beside the daemon's log when it has one (tmpfs on a router),
// else a daemon-owned subdirectory of the hook directory, outside the
// script's area.
func hookLogDir(logFile, hookDir string) string {
	if logFile != "" {
		return filepath.Join(filepath.Dir(logFile), "hooks")
	}
	return filepath.Join(hookDir, "log")
}

func (r *hookRunner) snapshotPath() string { return filepath.Join(r.dir, "core_snapshot.json") }
func (r *hookRunner) lockPath() string     { return filepath.Join(r.dir, ".lock") }

// statusView is the `hooks` object of GET /admin/status.
func (r *hookRunner) statusView() map[string]any {
	view := map[string]any{"enabled": false}
	if r == nil {
		return view
	}
	view["enabled"] = r.enabled
	r.access.Lock()
	defer r.access.Unlock()
	for _, event := range hookEvents {
		outcome := *r.outcomes[event]
		view[string(event)] = outcome
	}
	return view
}

func (r *hookRunner) record(event hookEvent, outcome hookOutcome) {
	r.access.Lock()
	r.outcomes[event] = &outcome
	r.access.Unlock()
}

// configured reports whether the hook file is the admin's: present and not
// byte-for-byte the embedded stub. The detail names the skip reason.
func (r *hookRunner) configured(event hookEvent) (bool, string) {
	content, err := os.ReadFile(filepath.Join(r.dir, string(event)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, "not configured (no file)"
		}
		return false, "not configured (" + err.Error() + ")"
	}
	if bytes.Equal(content, hookStub(event)) {
		return false, "not configured (stub unchanged)"
	}
	return true, ""
}

// gate is the root-owned invariant before every run (SPEC 100 chain plus
// "regular and executable"): a script replaced after the service started
// must not slip through, so it is checked per run, not once.
func (r *hookRunner) gate(path string) error {
	if r.skipOwnerCheck {
		return nil
	}
	if err := checkRootOwnedChain(path, r.lstat); err != nil {
		return err
	}
	info, err := r.lstat(path)
	if err != nil {
		return err
	}
	if !info.mode.IsRegular() {
		return E.New(path, ": not a regular file")
	}
	if info.mode.Perm()&0o111 == 0 {
		return E.New(path, ": not executable")
	}
	return nil
}

// fire runs one event synchronously and records its outcome. The error is
// non-nil when the hook ran and failed, timed out or did not pass the gate —
// pre_start uses it as its fail-closed verdict; the post_* callers only log.
// A hook that is not configured is skipped with a Warn and no error.
func (r *hookRunner) fire(event hookEvent, origin hookOrigin, current, previous coreFacts, errorText string) error {
	if r == nil || !r.enabled {
		return nil
	}
	label := "lxd: hook " + string(event)
	if ok, detail := r.configured(event); !ok {
		r.record(event, hookOutcome{State: hookStateSkipped, Reason: origin.reason, FinishedAt: nowStamp(), Detail: detail})
		log.Warn(label, " skipped: ", detail)
		return nil
	}
	path := filepath.Join(r.dir, string(event))
	if err := r.gate(path); err != nil {
		r.record(event, hookOutcome{State: hookStateFailed, Reason: origin.reason, FinishedAt: nowStamp(), Detail: err.Error()})
		log.Error(label, " failed: ", err)
		return E.Cause(err, "hook ", string(event))
	}
	env := append(append([]string{}, r.baseEnv...),
		"LXD_EVENT="+string(event),
		"LXD_REASON="+origin.reason,
		"LXD_CLIENT="+origin.client,
		"LXD_CONFIG_SHA="+current.ConfigSHA,
		"LXD_CONFIG_SHA_PREV="+previous.ConfigSHA,
		"LXD_TUN_ADDRESS="+current.TunAddress,
		"LXD_TUN_ADDRESS_PREV="+previous.TunAddress,
		"LXD_TUN_INTERFACE="+current.TunInterface,
		"LXD_TUN_INTERFACE_PREV="+previous.TunInterface,
		"LXD_TUN_MTU="+current.TunMTU,
		"LXD_TUN_MTU_PREV="+previous.TunMTU,
		"LXD_TUN_AUTO_ROUTE="+current.TunAutoRoute,
		"LXD_TUN_AUTO_ROUTE_PREV="+previous.TunAutoRoute,
		"LXD_ERROR="+errorText,
	)
	logPath := filepath.Join(r.logDir, string(event)+".log")
	log.Info(label, " (reason ", origin.reason, ") started")
	startedAt := time.Now()
	exitCode, timedOut, err := withHookLock(r.lockPath(), func() (int, bool, error) {
		return r.run(path, env, logPath, r.timeout)
	})
	elapsed := time.Since(startedAt).Round(time.Millisecond)
	switch {
	case err != nil:
		r.record(event, hookOutcome{State: hookStateFailed, Reason: origin.reason, FinishedAt: nowStamp(), Detail: err.Error()})
		log.Error(label, " failed: ", err)
		return E.Cause(err, "hook ", string(event))
	case timedOut:
		detail := "timed out after " + r.timeout.String()
		r.record(event, hookOutcome{State: hookStateTimeout, Reason: origin.reason, FinishedAt: nowStamp(), Detail: detail})
		log.Error(label, " ", detail, ", killed", outputTail(logPath))
		return E.New("hook ", string(event), ": ", detail)
	case exitCode != 0:
		detail := "exit " + strconv.Itoa(exitCode)
		if first := firstLine(tailFile(logPath, hookLogTail)); first != "" {
			detail += ": " + first
		}
		r.record(event, hookOutcome{State: hookStateFailed, Reason: origin.reason, ExitCode: &exitCode, FinishedAt: nowStamp(), Detail: detail})
		log.Error(label, " failed: exit ", exitCode, outputTail(logPath))
		return E.New("hook ", string(event), ": ", detail)
	default:
		r.record(event, hookOutcome{State: hookStateOK, Reason: origin.reason, ExitCode: &exitCode, FinishedAt: nowStamp()})
		log.Info(label, " done in ", elapsed)
		return nil
	}
}

func outputTail(logPath string) string {
	tail := strings.TrimSpace(tailFile(logPath, hookLogTail))
	if tail == "" {
		return ""
	}
	return " (output tail)\n" + tail
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// preStart is the gate before a start: a failure cancels the start.
func (r *hookRunner) preStart(origin hookOrigin, current coreFacts) error {
	return r.fire(hookPreStart, origin, current, coreFacts{}, "")
}

// postStart announces a core that came up; core_snapshot.json is written first so
// a crash during the hook still leaves the record for recover.
func (r *hookRunner) postStart(origin hookOrigin, current coreFacts, errorText string) {
	r.writeState(current)
	_ = r.fire(hookPostStart, origin, current, coreFacts{}, errorText)
}

// postReload announces a swapped instance (apply, sighup, rollback).
func (r *hookRunner) postReload(origin hookOrigin, current, previous coreFacts, errorText string) {
	r.writeState(current)
	_ = r.fire(hookPostReload, origin, current, previous, errorText)
}

// postStop announces a core that is gone; core_snapshot.json is removed afterwards
// whatever the hook did.
func (r *hookRunner) postStop(origin hookOrigin, previous coreFacts, errorText string) {
	_ = r.fire(hookPostStop, origin, coreFacts{}, previous, errorText)
	r.removeState()
}

func (r *hookRunner) writeState(facts coreFacts) {
	if r == nil || !r.enabled {
		return
	}
	encoded, err := json.MarshalIndent(coreSnapshot{coreFacts: facts, StartedAt: nowStamp(), PID: os.Getpid()}, "", "  ")
	if err == nil {
		err = r.store.writeAtomic(r.snapshotPath(), append(encoded, '\n'))
	}
	if err != nil {
		log.Error(E.Cause(err, "lxd: hooks: write core_snapshot.json"))
	}
}

func (r *hookRunner) removeState() {
	if r == nil {
		return
	}
	if err := os.Remove(r.snapshotPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Error(E.Cause(err, "lxd: hooks: remove core_snapshot.json"))
	}
}

// recoverAtBoot runs before the first pre_start: a core_snapshot.json left by the
// previous process means it never reached post_stop (core panic, kill -9,
// power loss), so post_stop runs now with reason `recover` and the facts from
// the file. With hooks disabled a leftover file is removed with a Warn: a
// disabled daemon runs no script, so the system DNS may still point at the
// dead tun and the admin has to restore it by hand; the record must not fire
// a recover for a long-gone core once hooks are re-enabled.
func (r *hookRunner) recoverAtBoot() {
	if r == nil {
		return
	}
	content, err := os.ReadFile(r.snapshotPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Error(E.Cause(err, "lxd: hooks: read core_snapshot.json"))
		}
		return
	}
	if !r.enabled {
		log.Warn("lxd: hooks disabled, dropping stale core_snapshot.json from a run that did not reach post_stop — post_stop(recover) is not run, restore the system state by hand if the previous run changed it")
		r.removeState()
		return
	}
	var snapshot coreSnapshot
	if err = json.Unmarshal(content, &snapshot); err != nil {
		log.Error(E.Cause(err, "lxd: hooks: decode core_snapshot.json; running post_stop(recover) with empty facts"))
	}
	log.Warn("lxd: hooks: core_snapshot.json from a previous run (pid ", snapshot.PID, ", started ", snapshot.StartedAt, ") — the previous daemon did not reach post_stop, recovering")
	r.postStop(hookOrigin{reason: hookReasonRecover}, snapshot.coreFacts, "")
}

// ---- facts ----

// tunInboundView is the slice of a tun inbound the hooks need, decoded from
// the config JSON tree without option.Options: a full decode would drag the
// registration of every inbound into the daemon's own check.
type tunInboundView struct {
	Type          string                     `json:"type"`
	Tag           string                     `json:"tag"`
	InterfaceName string                     `json:"interface_name"`
	MTU           uint32                     `json:"mtu"`
	Address       badoption.Listable[string] `json:"address"`
	AutoRoute     bool                       `json:"auto_route"`
}

type configInboundsView struct {
	Inbounds []tunInboundView `json:"inbounds"`
}

// tunMTUDefault mirrors the core's default when `mtu` is not set.
const tunMTUDefault = 9000

// tunInbounds lists the tun inbounds of a config in order, with their tags
// as the box names them (an explicit tag, else the index).
func tunInbounds(content string) []tunInboundView {
	view, err := sjson.UnmarshalExtended[configInboundsView]([]byte(content))
	if err != nil {
		return nil
	}
	var tuns []tunInboundView
	for index, inbound := range view.Inbounds {
		if inbound.Type != "tun" {
			continue
		}
		if inbound.Tag == "" {
			inbound.Tag = strconv.Itoa(index)
		}
		tuns = append(tuns, inbound)
	}
	return tuns
}

// tunNameSource answers the actual interface names of the tun inbounds of
// the running box, by tag; the real service implements it, the unit tests'
// fake may.
type tunNameSource interface {
	TunInterfaceNames(tags []string) []string
}

// configFacts derives the facts from a config: addresses, mtu and auto_route
// from the tree; interface names from `names` when the caller has the live
// box, else only the explicit interface_name of each tun inbound.
func configFacts(content string, names tunNameSource) coreFacts {
	facts := coreFacts{ConfigSHA: contentSHA(content)}
	tuns := tunInbounds(content)
	if len(tuns) == 0 {
		return facts
	}
	var addresses, interfaces, mtus, autoRoutes []string
	tags := make([]string, 0, len(tuns))
	for _, inbound := range tuns {
		addresses = append(addresses, inbound.Address...)
		interfaces = append(interfaces, inbound.InterfaceName)
		mtu := inbound.MTU
		if mtu == 0 {
			mtu = tunMTUDefault
		}
		mtus = append(mtus, strconv.FormatUint(uint64(mtu), 10))
		if inbound.AutoRoute {
			autoRoutes = append(autoRoutes, "1")
		} else {
			autoRoutes = append(autoRoutes, "0")
		}
		tags = append(tags, inbound.Tag)
	}
	if names != nil {
		if actual := names.TunInterfaceNames(tags); len(actual) == len(tags) {
			for index, name := range actual {
				if name != "" {
					interfaces[index] = name
				}
			}
		}
	}
	facts.TunAddress = strings.Join(addresses, ",")
	facts.TunInterface = joinOrEmpty(interfaces)
	facts.TunMTU = strings.Join(mtus, ",")
	facts.TunAutoRoute = strings.Join(autoRoutes, ",")
	return facts
}

// joinOrEmpty joins the list with commas, but a list with nothing known in
// it reads "" rather than ",".
func joinOrEmpty(items []string) string {
	for _, item := range items {
		if item != "" {
			return strings.Join(items, ",")
		}
	}
	return ""
}

// ---- stubs ----

// hookStubSpec describes one event for its stub comment.
type hookStubSpec struct {
	when    string
	reasons string
	failure string
	current string // the LXD_TUN_INTERFACE note of the current facts; "" = no current facts
	prev    string // the LXD_TUN_INTERFACE_PREV note; "" = no previous facts
	errVar  string // the LXD_ERROR note; "" = not set
}

var hookStubSpecs = map[hookEvent]hookStubSpec{
	hookPreStart: {
		when:    "before the core starts (there is no core yet)",
		reasons: "boot | start",
		failure: "a non-zero exit CANCELS the start (fail-closed); no side effects belong here — this is a precondition gate",
		current: "explicit interface_name only, usually empty: the actual name does not exist yet",
	},
	hookPostStart: {
		when:    "after the core came up — tun and routes already exist",
		reasons: "boot | start",
		failure: "a non-zero exit is logged and marked in /admin/status; the core keeps running",
		current: "actual interface names (utunN / tunN)",
	},
	hookPostReload: {
		when:    "after the instance was swapped in the live process — the core never went away, only its facts changed",
		reasons: "apply | sighup | rollback (the new config did not come up, last-good is up again)",
		failure: "a non-zero exit is logged and marked in /admin/status; the core keeps running",
		current: "actual interface names of the new instance",
		prev:    "actual interface names of the old instance, taken before it was closed",
		errVar:  "set on rollback: why the new config was rejected",
	},
	hookPostStop: {
		when:    "after the core is gone",
		reasons: "stop | shutdown (SIGTERM, update, service restart) | fatal (the start failed after pre_start passed, or the rollback failed too) | recover (the previous daemon process died without reaching post_stop; facts from hooks/core_snapshot.json)",
		failure: "a non-zero exit is logged only; nothing blocks the stop",
		prev:    "actual interface names of the instance that was closed; on fatal only an explicit interface_name",
		errVar:  "set on fatal: the core's error",
	},
}

// hookStub is the reference stub of one event, embedded in the binary: a
// file byte-for-byte equal to it counts as not configured. Not empty — an
// empty exec returns ENOEXEC, indistinguishable from a broken hook — and it
// documents the variables of ITS event where the admin will write the script.
func hookStub(event hookEvent) []byte {
	spec := hookStubSpecs[event]
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# lxd hook \"" + string(event) + "\": runs " + spec.when + ".\n")
	b.WriteString("# Replace this file with your script. While it is byte-for-byte this stub (or absent),\n")
	b.WriteString("# the daemon skips the event as \"not configured\". Outcome: " + spec.failure + ".\n")
	b.WriteString("#\n")
	b.WriteString("# Runs synchronously as the daemon's user (root), cwd /, stdin /dev/null; stdout and\n")
	b.WriteString("# stderr go to hooks/" + string(event) + ".log beside the daemon log. Must exit within\n")
	b.WriteString("# $LXD_HOOK_TIMEOUT seconds — the daemon then kills the whole process group. A background\n")
	b.WriteString("# tail (\"nohup cmd &\") is not waited for and dies with the daemon unless started with setsid;\n")
	b.WriteString("# whatever the DNS or the gate depends on must finish before exit. post_* hooks must be\n")
	b.WriteString("# idempotent and recover from the script's own snapshot in $LXD_HOOK_DIR: after a crash of\n")
	b.WriteString("# the daemon process post_stop runs at the next boot with LXD_REASON=recover.\n")
	b.WriteString("#\n")
	b.WriteString("# Environment (empty = unknown or not applicable):\n")
	b.WriteString("#   LXD_EVENT=" + string(event) + "\n")
	b.WriteString("#   LXD_REASON              " + spec.reasons + "\n")
	b.WriteString("#   LXD_CLIENT              client name when a REST request caused the transition, else empty\n")
	b.WriteString("#   LXD_PLATFORM, LXD_VERSION, LXD_INIT_SYSTEM, LXD_STATE_DIR, LXD_BIN, PATH\n")
	b.WriteString("#                           as for daemon.json commands\n")
	b.WriteString("#   LXD_LOG_FILE            the daemon log; empty when run in a terminal\n")
	b.WriteString("#   LXD_HOOK_DIR            this directory — keep the snapshot of the system state here\n")
	b.WriteString("#   LXD_HOOK_TIMEOUT        the budget in seconds\n")
	if spec.current != "" {
		b.WriteString("#   LXD_CONFIG_SHA          sha256 of the config of the current core\n")
		b.WriteString("#   LXD_TUN_ADDRESS         addresses of all tun inbounds, comma-separated, v4 and v6, config order\n")
		b.WriteString("#   LXD_TUN_INTERFACE       " + spec.current + "\n")
		b.WriteString("#   LXD_TUN_MTU             mtu per tun inbound, comma-separated (9000 when not set)\n")
		b.WriteString("#   LXD_TUN_AUTO_ROUTE      1/0 per tun inbound — with 0 pointing the system DNS at the tun is pointless\n")
	}
	if spec.prev != "" {
		b.WriteString("#   LXD_CONFIG_SHA_PREV     sha256 of the config of the previous core\n")
		b.WriteString("#   LXD_TUN_ADDRESS_PREV    addresses of the previous core's tun inbounds\n")
		b.WriteString("#   LXD_TUN_INTERFACE_PREV  " + spec.prev + "\n")
		b.WriteString("#   LXD_TUN_MTU_PREV        mtu per previous tun inbound\n")
		b.WriteString("#   LXD_TUN_AUTO_ROUTE_PREV 1/0 per previous tun inbound\n")
	}
	if spec.errVar != "" {
		b.WriteString("#   LXD_ERROR               " + spec.errVar + "\n")
	}
	b.WriteString("#   (the LXD_* variables of the other events are set too, empty for this one)\n")
	b.WriteString("exit 0\n")
	return []byte(b.String())
}
