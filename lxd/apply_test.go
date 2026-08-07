//go:build with_lx_command

package lxd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

type fakeReloader struct {
	calls      []string
	failFor    map[string]error
	closeCalls int
}

func (f *fakeReloader) StartOrReloadService(ctx context.Context, content string, options *daemon.OverrideOptions) error {
	f.calls = append(f.calls, content)
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
		ctx:      context.Background(),
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
