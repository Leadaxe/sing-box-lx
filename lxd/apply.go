//go:build with_lx_command

package lxd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func execSelfCheck(ctx context.Context, configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return E.Cause(err, "locate own binary")
	}
	ctx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "check", "--disable-color", "-c", configPath).CombinedOutput()
	if err != nil {
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
	ctx      context.Context
	service  reloader
	store    *store
	validate validateFunc
	clients  *clientRegistry // nil when TLS/mTLS is disabled
	// serverFingerprint and advertiseAddr build enrollment invites; set only
	// when TLS is enabled.
	serverFingerprint string
	advertiseAddr     string

	applyAccess sync.Mutex

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

	candidatePath, err := c.store.StageCandidate(content)
	if err != nil {
		// A disk failure is not a config verdict — surface it as such, not 422.
		return applyResult{Outcome: applyError, Err: E.Cause(err, "stage candidate")}
	}
	if err = c.validate(ctx, candidatePath); err != nil {
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
	lastGood, loaded, _ := c.store.LoadLastGood()
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
	c.setActive(lastGood, applyErr.Error())
	return applyResult{Outcome: applyFailed, RolledBack: true, Err: applyErr}
}

// Rollback re-applies last-good directly, skipping validation: it is the
// emergency path and its content already passed the pipeline once.
func (c *controller) Rollback(ctx context.Context) (applyResult, bool) {
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	lastGood, loaded, _ := c.store.LoadLastGood()
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
