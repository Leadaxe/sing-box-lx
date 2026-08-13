//go:build with_lxd

package lxd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

// reloader is the slice of daemon.StartedService the apply pipeline needs;
// an interface so the pipeline is unit-testable without booting a real box.
type reloader interface {
	StartOrReloadService(ctx context.Context, profileContent string, options *daemon.OverrideOptions) error
	CloseService() error
}

// validateFunc checks a staged candidate config. The default implementation
// re-executes this binary as `check -c <path>`: validation crashes are
// isolated in the subprocess and can never take the daemon down (the reason
// the upstream desktop daemon delegates CheckConfig to a worker process).
type validateFunc func(ctx context.Context, configPath string) error

const validateTimeout = 10 * time.Second

// infraError marks a validation failure that is NOT a verdict on the config —
// the validator could not run (binary missing, exec failed, request aborted).
// Apply maps it to applyError (HTTP 500), never 422: a 422 tells the launcher
// "your config is broken", which must not be said when nothing was checked.
type infraError struct{ inner error }

func (e infraError) Error() string { return e.inner.Error() }
func (e infraError) Unwrap() error { return e.inner }

func execSelfCheck(ctx context.Context, configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return infraError{E.Cause(err, "locate own binary")}
	}
	checkCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, executable, "check", "--disable-color", "-c", configPath).CombinedOutput()
	if err != nil {
		// The parent request being canceled aborts the check mid-flight; that
		// says nothing about the config. Our own deadline firing does: a check
		// that cannot finish in validateTimeout is a config verdict.
		if ctx.Err() != nil {
			return infraError{E.Cause(ctx.Err(), "validation aborted")}
		}
		if checkCtx.Err() != nil {
			return E.New("validation timed out after ", validateTimeout.String())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return infraError{E.Cause(err, "run validator")}
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return E.New(message)
	}
	return nil
}

type applyOutcome int

const (
	applyApplied  applyOutcome = iota
	applyRejected              // config failed validation — the instance was never touched
	applyFailed                // the swap failed at Start time (with or without rollback)
	applyError                 // infrastructure failure (disk, state store) — not a config verdict
)

type applyResult struct {
	Outcome    applyOutcome
	RolledBack bool
	Err        error
}

type controller struct {
	service   reloader
	store     *store
	resources *resourceStore // nil until SPEC 063 wiring; guards handle absence
	validate  validateFunc
	clients   *clientRegistry // nil when TLS/mTLS is disabled
	// serverFingerprint and advertiseAddr build enrollment invites; set only
	// when TLS is enabled.
	serverFingerprint string
	advertiseAddr     string

	// Immutable identity facts served by GET /admin/info (set once in Run,
	// before the listener starts): clients discover the daemon's home and
	// version instead of hard-coding paths on their side.
	infoStateDir string
	// infoResourcesDir is the absolute <state_dir>/resources — the base the
	// resource REST plane reports in `path`, so a client copies it verbatim
	// into a config's `type: local, path:` (SPEC 063; absolute by owner
	// decision, matching /admin/info.state_dir).
	infoResourcesDir string
	infoTLS          bool
	startedAt        time.Time

	// Observability plane (SPEC 065). memory is nil-safe only through
	// newController; profiles holds the mutable pprof state (in-flight
	// recording, block/mutex rates) and is deliberately per-daemon, never
	// persisted.
	memory   *memoryCache
	profiles profilePlane

	// clientInfo is the IP → device directory (SPEC 066); nil until wired, and
	// the routes are registered only when it is set.
	clientInfo *clientInfo

	// host is the host telemetry cache (SPEC 068), holding the previous CPU
	// and interface samples that percentages and rates are derived from.
	host *hostCache

	applyAccess sync.Mutex
	// closed is set (under applyAccess) by the daemon teardown: admin requests
	// that were queued behind the mutex must not touch the service afterwards.
	closed bool

	stateAccess      sync.Mutex
	activeContent    string
	activeSHA        string
	running          bool
	lastError        string
	interruptedApply bool
}

func contentSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// setActive records a successful swap: the daemon is running `content`.
// lastError is kept as informational history (e.g. the cause of a rollback)
// and never drives the status once running is true.
func (c *controller) setActive(content string, lastError string) {
	c.stateAccess.Lock()
	c.activeContent = content
	c.activeSHA = contentSHA(content)
	c.running = true
	c.lastError = lastError
	c.stateAccess.Unlock()
}

// setFatal records a swap that left no running instance behind: active
// content is cleared so /admin/config never reports a config that is not
// actually running.
func (c *controller) setFatal(err error) {
	c.stateAccess.Lock()
	c.activeContent = ""
	c.activeSHA = ""
	c.running = false
	c.lastError = err.Error()
	c.stateAccess.Unlock()
}

// Apply runs the full pipeline: stage → validate (subprocess, instance
// untouched on reject) → pending marker → swap instance → persist last-good,
// with an automatic rollback to last-good when the swap fails at Start time.
// Serialized: concurrent applies (REST + SIGHUP) queue behind the mutex.
func (c *controller) Apply(ctx context.Context, content string) applyResult {
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	if c.closed {
		return applyResult{Outcome: applyError, Err: E.New("daemon is shutting down")}
	}

	candidatePath, err := c.store.StageCandidate(content)
	if err != nil {
		// A disk failure is not a config verdict — surface it as such, not 422.
		return applyResult{Outcome: applyError, Err: E.Cause(err, "stage candidate")}
	}
	if err = c.validate(ctx, candidatePath); err != nil {
		var infra infraError
		if errors.As(err, &infra) {
			return applyResult{Outcome: applyError, Err: err}
		}
		return applyResult{Outcome: applyRejected, Err: err}
	}
	if err = c.store.SetPending(contentSHA(content)); err != nil {
		return applyResult{Outcome: applyError, Err: E.Cause(err, "persist pending marker")}
	}
	defer c.store.ClearPending()

	if err = c.service.StartOrReloadService(ctx, content, nil); err == nil {
		_ = c.store.SetWasRunning(true)
		if saveErr := c.store.SaveLastGood(content); saveErr != nil {
			// The config is live; a failed persist only degrades the next boot.
			c.setActive(content, E.Cause(saveErr, "persist last-good").Error())
			return applyResult{Outcome: applyApplied, Err: E.Cause(saveErr, "applied, but persisting last-good failed")}
		}
		c.setActive(content, "")
		return applyResult{Outcome: applyApplied}
	}

	applyErr := err
	lastGood, loaded, loadErr := c.store.LoadLastGood()
	if loadErr != nil {
		// Per the store contract an IO failure is not "no file" — but at this
		// point there is no instance either way; keep the cause visible.
		applyErr = E.Cause(applyErr, "read last-good for rollback: "+loadErr.Error())
	}
	if !loaded || lastGood == content {
		// Nothing distinct to roll back to (no last-good, or it is the very
		// config that just failed): no running instance remains.
		c.setFatal(applyErr)
		return applyResult{Outcome: applyFailed, Err: applyErr}
	}
	if rollbackErr := c.service.StartOrReloadService(ctx, lastGood, nil); rollbackErr != nil {
		c.setFatal(rollbackErr)
		return applyResult{Outcome: applyFailed, Err: E.Cause(applyErr, "rollback also failed: "+rollbackErr.Error())}
	}
	// The rollback left the core running: record the intent so a daemon
	// restart brings it back even if this was the very first apply.
	_ = c.store.SetWasRunning(true)
	c.setActive(lastGood, applyErr.Error())
	return applyResult{Outcome: applyFailed, RolledBack: true, Err: applyErr}
}

// resourceReferenced reports whether a config that must not lose the resource
// `name` currently references it — the active (running) config or the persisted
// last-good. It gates PUT/DELETE (SPEC 063 guard B2): a named resource is
// mutable, so overwriting or deleting one a live-or-recoverable config points at
// would make a config rollback partial (text reverts, bytes do not).
//
// Called under applyAccess so it cannot race an apply that adds a reference:
// either the apply's new activeContent is already visible here, or this check
// completed and the apply queues behind us.
//
// The test is a conservative substring match on `/resources/<name>`: it can
// false-positive (the token appearing in a comment or unrelated string) and
// answer "referenced" for a free name, which only ever costs an over-eager 409
// — never a silently broken rollback, the failure that actually matters.
func (c *controller) resourceReferenced(name string) (bool, error) {
	token := "/resources/" + name
	c.stateAccess.Lock()
	active := c.activeContent
	c.stateAccess.Unlock()
	if strings.Contains(active, token) {
		return true, nil
	}
	lastGood, loaded, err := c.store.LoadLastGood()
	if err != nil {
		// An unreadable last-good must not be treated as "not referenced": that
		// could green-light a delete that a recoverable config depends on.
		return false, E.Cause(err, "read last-good for reference check")
	}
	if loaded && strings.Contains(lastGood, token) {
		return true, nil
	}
	return false, nil
}

// Rollback re-applies last-good directly, skipping validation: it is the
// emergency path and its content already passed the pipeline once.
func (c *controller) Rollback(ctx context.Context) (applyResult, bool) {
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	if c.closed {
		return applyResult{Outcome: applyError, Err: E.New("daemon is shutting down")}, true
	}
	lastGood, loaded, loadErr := c.store.LoadLastGood()
	if loadErr != nil {
		// An unreadable last-good is an infrastructure failure, not "nothing
		// recorded" — a 404 would mislead the launcher into a full re-apply.
		return applyResult{Outcome: applyError, Err: E.Cause(loadErr, "read last-good")}, true
	}
	if !loaded {
		return applyResult{}, false
	}
	if err := c.service.StartOrReloadService(ctx, lastGood, nil); err != nil {
		c.setFatal(err)
		return applyResult{Outcome: applyFailed, Err: err}, true
	}
	_ = c.store.SetWasRunning(true)
	c.setActive(lastGood, "")
	return applyResult{Outcome: applyApplied}, true
}

// Start brings the core up from the recorded last-good without a new config —
// the counterpart to Stop, letting the launcher own the core's lifecycle
// independently of which config is loaded. Returns false when there is no
// last-good to start from.
func (c *controller) Start(ctx context.Context) (applyResult, bool) {
	return c.Rollback(ctx)
}

// Stop tears the core instance down while the daemon, its control channel and
// all client connections stay up. Records the stopped intent so a restart
// does not silently bring the core back.
func (c *controller) Stop() error {
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	if c.closed {
		return E.New("daemon is shutting down")
	}
	_ = c.store.SetWasRunning(false)
	if err := c.service.CloseService(); err != nil {
		return err
	}
	c.stateAccess.Lock()
	c.running = false
	c.activeContent = ""
	c.activeSHA = ""
	c.lastError = ""
	c.stateAccess.Unlock()
	return nil
}
