//go:build with_lx_command

package lxd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"
)

func TestRunBadListen(t *testing.T) {
	// Run must RETURN on a bad listen address; guard against a regression that
	// leaves it serving (or hanging) instead of failing fast.
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Listen:   "256.0.0.1:99999",
			StateDir: t.TempDir(),
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error for bad listen address")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return on a bad listen address")
	}
}

// The load-bearing property of SPEC 055, exercised against the real core: the
// control plane keeps answering across a successful apply, a broken apply
// (with automatic rollback, SPEC 056), and a stop. The daemon.StartedService
// here is the real thing — only the subprocess validator is stubbed out
// (the test binary has no `check` subcommand to re-exec).
func TestControlPlaneSurvivesBrokenApply(t *testing.T) {
	// The real core writes its cache-file (cache.db) into the working
	// directory; run from a temp dir so the suite never litters the repo.
	t.Chdir(t.TempDir())
	// The daemon package needs the sing service registry with the box
	// inbound/outbound registries — the same context the CLI builds for the
	// real daemon (cmd.go). Without it NewStartedService panics on reload.
	startedService := daemon.NewStartedService(daemon.ServiceOptions{
		Context:     include.Context(context.Background()),
		LogMaxLines: 100,
	})
	defer startedService.Close()
	defer startedService.CloseService()

	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := &controller{
		service:  startedService,
		store:    stateStore,
		validate: func(context.Context, string) error { return nil },
	}
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	post := func(path, body string) (int, string) {
		t.Helper()
		response, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		buffer := make([]byte, 4096)
		n, _ := response.Body.Read(buffer)
		return response.StatusCode, string(buffer[:n])
	}
	get := func(path string) (int, string) {
		t.Helper()
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		buffer := make([]byte, 4096)
		n, _ := response.Body.Read(buffer)
		return response.StatusCode, string(buffer[:n])
	}

	goodConfig := `{"log": {"disabled": true}}`
	if status, body := post("/admin/apply", goodConfig); status != http.StatusOK {
		t.Fatalf("apply of a valid config failed: %d %s", status, body)
	}
	if status, body := get("/admin/status"); status != http.StatusOK || !strings.Contains(body, `"status":"started"`) {
		t.Fatalf("expected started status, got %d %s", status, body)
	}

	// A config the stubbed validator waves through but the core rejects at
	// parse/start — the SPEC 056 rollback path, on the real core.
	brokenConfig := `{"outbounds": [{"type": "no-such-type", "tag": "x"}]}`
	status, body := post("/admin/apply", brokenConfig)
	if status != http.StatusInternalServerError || !strings.Contains(body, `"rolled_back":true`) {
		t.Fatalf("expected 500 with rolled_back:true, got %d %s", status, body)
	}

	// The control plane is alive and reports the rolled-back config as active.
	if status, body := get("/admin/status"); status != http.StatusOK ||
		!strings.Contains(body, `"status":"started"`) || !strings.Contains(body, "no-such-type") {
		t.Fatalf("after rollback: expected started status with the failure in last_error, got %d %s", status, body)
	}
	if status, body := get("/admin/config"); status != http.StatusOK || body != goodConfig {
		t.Fatalf("active config must be the rolled-back last-good, got %d %s", status, body)
	}

	// Stop tears the core down; the control plane still answers.
	if status, body := post("/admin/stop", ""); status != http.StatusOK {
		t.Fatalf("stop failed: %d %s", status, body)
	}
	if status, body := get("/admin/status"); status != http.StatusOK || !strings.Contains(body, `"status":"idle"`) {
		t.Fatalf("after stop: expected idle status, got %d %s", status, body)
	}
}
