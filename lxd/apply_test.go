//go:build with_lxd

package lxd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

type fakeReloader struct {
	calls      []string
	failFor    map[string]error
	closeCalls int
	// onStart, when set, observes each swap attempt — used to assert what the
	// pipeline guarantees at the moment the instance is touched.
	onStart func(content string)
}

func (f *fakeReloader) StartOrReloadService(ctx context.Context, content string, options *daemon.OverrideOptions) error {
	f.calls = append(f.calls, content)
	if f.onStart != nil {
		f.onStart(content)
	}
	return f.failFor[content]
}

func (f *fakeReloader) CloseService() error {
	f.closeCalls++
	return nil
}

func newTestController(t *testing.T, service *fakeReloader, validate validateFunc) *controller {
	t.Helper()
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if validate == nil {
		validate = func(ctx context.Context, configPath string) error { return nil }
	}
	return &controller{
		service:  service,
		store:    stateStore,
		validate: validate,
	}
}

func TestApplyRejectedLeavesInstanceUntouched(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, func(ctx context.Context, configPath string) error {
		return E.New("broken config")
	})
	result := control.Apply(context.Background(), `{"broken": true}`)
	if result.Outcome != applyRejected {
		t.Fatal("expected rejection")
	}
	if len(service.calls) != 0 {
		t.Fatal("rejected apply must not touch the instance")
	}
	if _, pending := control.store.PendingSHA(); pending {
		t.Fatal("pending marker must not survive a rejection")
	}
}

func TestApplySuccessPersistsLastGood(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyApplied {
		t.Fatal("expected success, got:", result.Err)
	}
	lastGood, loaded, _ := control.store.LoadLastGood()
	if !loaded || lastGood != `{"v": 1}` {
		t.Fatal("last-good not persisted")
	}
	if _, pending := control.store.PendingSHA(); pending {
		t.Fatal("pending marker must be cleared after success")
	}
}

func TestApplyStartFailureRollsBack(t *testing.T) {
	service := &fakeReloader{failFor: map[string]error{`{"v": 2}`: E.New("bind failed")}}
	control := newTestController(t, service, nil)
	if result := control.Apply(context.Background(), `{"v": 1}`); result.Outcome != applyApplied {
		t.Fatal("seed apply failed:", result.Err)
	}
	result := control.Apply(context.Background(), `{"v": 2}`)
	if result.Outcome != applyFailed || !result.RolledBack {
		t.Fatalf("expected rolled-back failure, got %+v", result)
	}
	if len(service.calls) != 3 || service.calls[2] != `{"v": 1}` {
		t.Fatalf("expected rollback call with last-good, calls: %v", service.calls)
	}
	if lastGood, _, _ := control.store.LoadLastGood(); lastGood != `{"v": 1}` {
		t.Fatal("last-good must stay at the previous config")
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if !control.running || control.activeContent != `{"v": 1}` {
		t.Fatal("controller must report the rolled-back config as running")
	}
	if control.lastError == "" {
		t.Fatal("rollback cause must stay visible")
	}
}

func TestApplyStartFailureWithoutLastGoodIsFatal(t *testing.T) {
	service := &fakeReloader{failFor: map[string]error{`{"v": 1}`: E.New("bind failed")}}
	control := newTestController(t, service, nil)
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyFailed || result.RolledBack {
		t.Fatalf("expected plain failure, got %+v", result)
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if control.running {
		t.Fatal("controller must not report running after a failed first apply")
	}
}

// The crash invariant of SPEC 056: the pending marker must be on disk BEFORE
// the instance is touched, so a process death mid-swap is detectable on the
// next boot. Deleting the SetPending call must fail this test.
func TestApplySetsPendingBeforeSwap(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	pendingAtSwap := false
	service.onStart = func(string) {
		_, pendingAtSwap = control.store.PendingSHA()
	}
	if result := control.Apply(context.Background(), `{"v": 1}`); result.Outcome != applyApplied {
		t.Fatal("apply failed:", result.Err)
	}
	if !pendingAtSwap {
		t.Fatal("pending marker must be persisted before the instance swap")
	}
}

// A failed swap whose last-good IS the failed config must not "roll back" to
// the very config that just failed — that path ends fatal, with one swap call.
func TestApplyFailedConfigEqualsLastGoodIsFatal(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	if result := control.Apply(context.Background(), `{"v": 1}`); result.Outcome != applyApplied {
		t.Fatal("seed apply failed:", result.Err)
	}
	service.failFor = map[string]error{`{"v": 1}`: E.New("bind failed")}
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyFailed || result.RolledBack {
		t.Fatalf("expected non-rolled-back failure, got %+v", result)
	}
	if len(service.calls) != 2 {
		t.Fatalf("no rollback swap may be attempted with an identical last-good, calls: %v", service.calls)
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if control.running || control.activeContent != "" {
		t.Fatal("no instance must be reported after the failure")
	}
}

// SaveLastGood failing after a successful swap is a degraded success: the
// config is live (200), but the warning must be surfaced.
func TestApplySucceedsWithWarningWhenPersistFails(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	// Make last_good.json unwritable by occupying its path with a directory:
	// writeAtomic's rename over a directory fails.
	if err := os.Mkdir(control.store.lastGoodPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyApplied {
		t.Fatalf("swap succeeded, so the apply must report applied, got %+v", result)
	}
	if result.Err == nil {
		t.Fatal("the failed last-good persist must be surfaced as a warning")
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if !control.running || control.activeContent != `{"v": 1}` {
		t.Fatal("the applied config must be reported as running")
	}
	if control.lastError == "" {
		t.Fatal("the persist failure must stay visible in lastError")
	}
}

// Rollback failing to READ last-good is an infrastructure failure (500), not
// "nothing recorded" (404): a 404 would push the launcher into a re-apply.
func TestRollbackUnreadableLastGoodIsError(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	if err := os.Mkdir(control.store.lastGoodPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	result, found := control.Rollback(context.Background())
	if !found {
		t.Fatal("an unreadable last-good must not be reported as absent")
	}
	if result.Outcome != applyError {
		t.Fatalf("expected applyError, got %+v", result)
	}
	if len(service.calls) != 0 {
		t.Fatal("the instance must not be touched when last-good cannot be read")
	}
}

// Validation failures that are not a config verdict (the validator could not
// run at all) must map to applyError, never applyRejected: a 422 would tell
// the launcher its config is broken when nothing was actually checked.
func TestApplyValidatorInfraFailureIsError(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, func(ctx context.Context, configPath string) error {
		return infraError{E.New("validator binary missing")}
	})
	result := control.Apply(context.Background(), `{"v": 1}`)
	if result.Outcome != applyError {
		t.Fatalf("expected applyError for an infra failure, got %+v", result)
	}
	if len(service.calls) != 0 {
		t.Fatal("instance must be untouched when validation could not run")
	}
}

// After teardown marked the controller closed, queued admin requests must not
// resurrect the core behind the dying daemon's back.
func TestClosedControllerRefusesLifecycle(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	control.applyAccess.Lock()
	control.closed = true
	control.applyAccess.Unlock()
	if result := control.Apply(context.Background(), `{"v": 1}`); result.Outcome != applyError {
		t.Fatalf("apply after close must fail, got %+v", result)
	}
	if result, _ := control.Rollback(context.Background()); result.Outcome != applyError {
		t.Fatalf("rollback after close must fail, got %+v", result)
	}
	if err := control.Stop(); err == nil {
		t.Fatal("stop after close must fail")
	}
	if len(service.calls) != 0 || service.closeCalls != 0 {
		t.Fatal("a closed controller must not touch the service")
	}
}

// Applies are serialized by applyAccess; run a burst under -race to keep the
// property honest.
func TestConcurrentAppliesSerialize(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	var group sync.WaitGroup
	for index := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			control.Apply(context.Background(), fmt.Sprintf(`{"v": %d}`, index))
		}()
	}
	group.Wait()
	if len(service.calls) != 8 {
		t.Fatalf("all applies must reach the service exactly once, calls: %d", len(service.calls))
	}
	control.stateAccess.Lock()
	defer control.stateAccess.Unlock()
	if !control.running || control.activeContent == "" {
		t.Fatal("the last completed apply must leave a running instance")
	}
}

func TestAdminAuth(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler("s3cret"))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/status", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("expected 401 without bearer, got", response.StatusCode)
	}

	request.Header.Set("Authorization", "Bearer s3cret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("expected 200 with bearer, got", response.StatusCode)
	}
}

func TestAdminApplyEmptyBody(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	response, err := http.Post(server.URL+"/admin/apply", "application/json", strings.NewReader("  "))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("expected 400 for empty body, got", response.StatusCode)
	}
}
