//go:build with_lxd

package lxd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	E "github.com/sagernet/sing/common/exceptions"
)

func TestApplyRollbackAlsoFailsIsFatal(t *testing.T) {
	// v1 applies fine; v2 fails; last-good (v1) also fails on the rollback swap.
	service := &fakeReloader{failFor: map[string]error{}}
	control := newTestController(t, service, nil)
	if r := control.Apply(context.Background(), `{"v": 1}`); r.Outcome != applyApplied {
		t.Fatal("seed failed")
	}
	// Now make BOTH the new config and the rollback target fail.
	service.failFor[`{"v": 2}`] = E.New("bind failed")
	service.failFor[`{"v": 1}`] = E.New("rollback bind failed")
	result := control.Apply(context.Background(), `{"v": 2}`)
	if result.Outcome != applyFailed || result.RolledBack {
		t.Fatalf("expected fatal non-rolled-back, got %+v", result)
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if control.running || control.activeContent != "" {
		t.Fatal("no instance must remain after both swaps fail")
	}
}

func TestApplyErrorOnStageFailure(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	// Break the state dir so StageCandidate fails.
	os.RemoveAll(control.store.dir)
	if err := os.WriteFile(control.store.dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyError {
		t.Fatalf("disk failure must be applyError, got %v", result.Outcome)
	}
	if len(service.calls) != 0 {
		t.Fatal("instance must be untouched on stage failure")
	}
}

func TestStopRecordsStoppedAndTearsDown(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	if r := control.Apply(context.Background(), `{"v": 1}`); r.Outcome != applyApplied {
		t.Fatal("apply failed")
	}
	if running, recorded := control.store.WasRunning(); !running || !recorded {
		t.Fatal("apply must record was-running")
	}
	if err := control.Stop(); err != nil {
		t.Fatal(err)
	}
	if service.closeCalls != 1 {
		t.Fatal("Stop must close the service")
	}
	running, recorded := control.store.WasRunning()
	if running {
		t.Fatal("Stop must record stopped")
	}
	if !recorded {
		t.Fatal("Stop must record an explicit intent, not wipe the file — fresh state boots the core, a stop must not")
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if control.running || control.activeContent != "" {
		t.Fatal("Stop must clear active state")
	}
}

func TestStartFromLastGood(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	// No last-good yet.
	if _, found := control.Start(context.Background()); found {
		t.Fatal("Start must report not-found with no last-good")
	}
	if r := control.Apply(context.Background(), `{"v": 1}`); r.Outcome != applyApplied {
		t.Fatal("apply failed")
	}
	control.Stop()
	result, found := control.Start(context.Background())
	if !found || result.Outcome != applyApplied {
		t.Fatalf("Start from last-good failed: found=%v %+v", found, result)
	}
	if running, _ := control.store.WasRunning(); !running {
		t.Fatal("Start must record was-running")
	}
}

func TestBootstrapPrefersLastGoodOverSeed(t *testing.T) {
	dir := t.TempDir()
	stateStore, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = stateStore.SaveLastGood(`{"from":"last-good"}`); err != nil {
		t.Fatal(err)
	}
	stateStore.SetWasRunning(true)
	seedPath := filepath.Join(dir, "seed.json")
	os.WriteFile(seedPath, []byte(`{"from":"seed"}`), 0o644)

	service := &fakeReloader{}
	control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
	control.applyAccess.Lock()
	bootstrap(context.Background(), control, stateStore, Options{ConfigPath: seedPath, StateDir: dir})
	control.applyAccess.Unlock()

	if len(service.calls) != 1 || service.calls[0] != `{"from":"last-good"}` {
		t.Fatalf("bootstrap must prefer last-good, calls: %v", service.calls)
	}
}

func TestBootstrapDoesNotRunWhenStopped(t *testing.T) {
	dir := t.TempDir()
	stateStore, _ := newStore(dir)
	stateStore.SaveLastGood(`{"v":1}`)
	// An explicit stop was recorded — the restart must not resurrect the core.
	stateStore.SetWasRunning(false)
	service := &fakeReloader{}
	control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
	control.applyAccess.Lock()
	bootstrap(context.Background(), control, stateStore, Options{StateDir: dir})
	control.applyAccess.Unlock()
	if len(service.calls) != 0 {
		t.Fatal("stopped run-state must not start the core")
	}
}

// Fresh state (no run intent ever recorded) with a boot config must start the
// core: `lxd -c seed.json` on a clean host works without --run (SPEC 056), and
// a 056-era state dir that predates the run-state file keeps booting last-good.
func TestBootstrapFreshStateWithConfigRuns(t *testing.T) {
	t.Run("seed", func(t *testing.T) {
		dir := t.TempDir()
		stateStore, _ := newStore(dir)
		seedPath := filepath.Join(dir, "seed.json")
		os.WriteFile(seedPath, []byte(`{"from":"seed"}`), 0o644)
		service := &fakeReloader{}
		control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
		control.applyAccess.Lock()
		bootstrap(context.Background(), control, stateStore, Options{ConfigPath: seedPath, StateDir: dir})
		control.applyAccess.Unlock()
		if len(service.calls) != 1 || service.calls[0] != `{"from":"seed"}` {
			t.Fatalf("fresh state with an explicit seed must boot the core, calls: %v", service.calls)
		}
		if lastGood, loaded, _ := stateStore.LoadLastGood(); !loaded || lastGood != `{"from":"seed"}` {
			t.Fatal("first successful boot must record the seed as last-good")
		}
	})
	t.Run("last-good from 056-era dir", func(t *testing.T) {
		dir := t.TempDir()
		stateStore, _ := newStore(dir)
		stateStore.SaveLastGood(`{"from":"last-good"}`)
		service := &fakeReloader{}
		control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
		control.applyAccess.Lock()
		bootstrap(context.Background(), control, stateStore, Options{StateDir: dir})
		control.applyAccess.Unlock()
		if len(service.calls) != 1 || service.calls[0] != `{"from":"last-good"}` {
			t.Fatalf("a state dir predating run-state must keep booting last-good, calls: %v", service.calls)
		}
	})
}

func TestBootstrapConfigForceOverridesLastGood(t *testing.T) {
	dir := t.TempDir()
	stateStore, _ := newStore(dir)
	stateStore.SaveLastGood(`{"from":"last-good"}`)
	stateStore.SetWasRunning(true)
	forcePath := filepath.Join(dir, "force.json")
	os.WriteFile(forcePath, []byte(`{"from":"force"}`), 0o644)

	service := &fakeReloader{}
	control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
	control.applyAccess.Lock()
	bootstrap(context.Background(), control, stateStore, Options{ConfigForce: forcePath, StateDir: dir})
	control.applyAccess.Unlock()

	if len(service.calls) != 1 || service.calls[0] != `{"from":"force"}` {
		t.Fatalf("config-force must win, calls: %v", service.calls)
	}
}

func TestBootstrapNeverBootsCandidate(t *testing.T) {
	dir := t.TempDir()
	stateStore, _ := newStore(dir)
	stateStore.SaveLastGood(`{"from":"last-good"}`)
	stateStore.SetWasRunning(true)
	stateStore.StageCandidate(`{"from":"candidate"}`)
	stateStore.SetPending("deadbeef")

	service := &fakeReloader{}
	control := &controller{service: service, store: stateStore, validate: func(context.Context, string) error { return nil }}
	control.applyAccess.Lock()
	bootstrap(context.Background(), control, stateStore, Options{StateDir: dir})
	control.applyAccess.Unlock()

	if len(service.calls) != 1 || service.calls[0] != `{"from":"last-good"}` {
		t.Fatalf("candidate must never boot, calls: %v", service.calls)
	}
	if !control.interruptedApply {
		t.Fatal("interrupted apply must be flagged")
	}
	if _, pending := stateStore.PendingSHA(); pending {
		t.Fatal("pending marker must be cleared on boot")
	}
}
