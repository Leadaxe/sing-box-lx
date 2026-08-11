//go:build with_lx_command

package lxd

// admin_test.go covers the HTTP contract of the launcher-facing admin plane:
// status codes and JSON fields for every apply outcome, the lifecycle routes,
// the mTLS certificate pin, enrollment, and the loopback-only operator routes.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// adminRequest hits a route on an httptest server and decodes the JSON body.
func adminRequest(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("non-JSON response %q: %v", raw, err)
		}
	}
	return response.StatusCode, payload
}

// serveAdmin pushes a crafted request straight through the handler, so the
// test controls RemoteAddr and the TLS connection state exactly.
func serveAdmin(t *testing.T, handler http.Handler, request *http.Request) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("non-JSON response %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, payload
}

func TestAdminApplyApplied(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`)
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	if payload["applied"] != true {
		t.Fatalf("expected applied=true, payload: %v", payload)
	}
	if payload["active_sha256"] != contentSHA(`{"v": 1}`) {
		t.Fatalf("active_sha256 must be the SHA of the exact body, payload: %v", payload)
	}
}

func TestAdminApplyRejectedIs422(t *testing.T) {
	// A plain validation error is a verdict on the config: 422, never 500.
	control := newTestController(t, &fakeReloader{}, func(context.Context, string) error {
		return E.New("broken config")
	})
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"broken": true}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatal("expected 422 for a rejected config, got", status)
	}
	if payload["applied"] != false || payload["rolled_back"] != false {
		t.Fatalf("rejection must report applied=false rolled_back=false, payload: %v", payload)
	}
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "broken config") {
		t.Fatalf("validation verdict must reach the launcher, payload: %v", payload)
	}
}

func TestAdminApplyInfraErrorIs500(t *testing.T) {
	// An infraError from the validator means nothing was checked: the launcher
	// must see 500 ("try again"), not 422 ("your config is broken").
	control := newTestController(t, &fakeReloader{}, func(context.Context, string) error {
		return infraError{E.New("boom")}
	})
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`)
	if status != http.StatusInternalServerError {
		t.Fatal("expected 500 for an infra failure, got", status)
	}
	if payload["applied"] != false || payload["rolled_back"] != false {
		t.Fatalf("infra failure must report applied=false rolled_back=false, payload: %v", payload)
	}
	errorText, _ := payload["error"].(string)
	if !strings.Contains(errorText, "boom") {
		t.Fatalf("infra cause must stay visible, payload: %v", payload)
	}
}

func TestAdminApplyStartFailureReportsRollback(t *testing.T) {
	service := &fakeReloader{failFor: map[string]error{`{"v": 2}`: E.New("bind failed")}}
	control := newTestController(t, service, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`); status != http.StatusOK {
		t.Fatal("seed apply failed with", status)
	}
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 2}`)
	if status != http.StatusInternalServerError {
		t.Fatal("expected 500 for a failed swap, got", status)
	}
	if payload["applied"] != false || payload["rolled_back"] != true {
		t.Fatalf("successful rollback must be reported, payload: %v", payload)
	}
	if errorText, _ := payload["error"].(string); errorText == "" {
		t.Fatalf("swap failure cause must reach the launcher, payload: %v", payload)
	}
}

func TestAdminApplyStartFailureWithoutLastGood(t *testing.T) {
	service := &fakeReloader{failFor: map[string]error{`{"v": 1}`: E.New("bind failed")}}
	control := newTestController(t, service, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`)
	if status != http.StatusInternalServerError {
		t.Fatal("expected 500, got", status)
	}
	if payload["applied"] != false || payload["rolled_back"] != false {
		t.Fatalf("no rollback happened and none must be claimed, payload: %v", payload)
	}
}

func TestAdminRollbackEndpoint(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	// No last-good recorded yet: 404, so the launcher knows to do a full apply.
	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/rollback", ""); status != http.StatusNotFound {
		t.Fatal("expected 404 without last-good, got", status)
	}

	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`); status != http.StatusOK {
		t.Fatal("apply failed with", status)
	}
	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/rollback", "")
	if status != http.StatusOK || payload["applied"] != true {
		t.Fatalf("rollback to last-good must succeed, got %d %v", status, payload)
	}

	// An unreadable last-good is an infrastructure failure: 500, NOT the 404
	// that would mislead the launcher into a full re-apply.
	path := control.store.lastGoodPath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	status, payload = adminRequest(t, http.MethodPost, server.URL+"/admin/rollback", "")
	if status != http.StatusInternalServerError {
		t.Fatal("expected 500 for an unreadable last-good, got", status)
	}
	if errorText, _ := payload["error"].(string); errorText == "" {
		t.Fatalf("read failure cause must be reported, payload: %v", payload)
	}
}

func TestAdminStartStopEndpoints(t *testing.T) {
	service := &fakeReloader{}
	control := newTestController(t, service, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/start", ""); status != http.StatusNotFound {
		t.Fatal("expected 404 starting with no last-good, got", status)
	}
	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`); status != http.StatusOK {
		t.Fatal("apply failed with", status)
	}

	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/stop", "")
	if status != http.StatusOK || payload["stopped"] != true {
		t.Fatalf("stop must succeed, got %d %v", status, payload)
	}
	running, recorded := control.store.WasRunning()
	if running || !recorded {
		t.Fatalf("stop must record an explicit stopped intent, got running=%v recorded=%v", running, recorded)
	}

	status, payload = adminRequest(t, http.MethodPost, server.URL+"/admin/start", "")
	if status != http.StatusOK || payload["started"] != true {
		t.Fatalf("start from last-good must succeed, got %d %v", status, payload)
	}
	if last := service.calls[len(service.calls)-1]; last != `{"v": 1}` {
		t.Fatal("start must boot the recorded last-good, got", last)
	}
}

func TestAdminStatusEndpoint(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/status", "")
	if status != http.StatusOK || payload["status"] != "idle" {
		t.Fatalf("fresh controller must report idle, got %d %v", status, payload)
	}
	if payload["active_sha256"] != "" || payload["interrupted_apply"] != false {
		t.Fatalf("fresh controller must have empty active state, payload: %v", payload)
	}

	if status, _ = adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`); status != http.StatusOK {
		t.Fatal("apply failed with", status)
	}
	_, payload = adminRequest(t, http.MethodGet, server.URL+"/admin/status", "")
	if payload["status"] != "started" {
		t.Fatalf("expected started after apply, payload: %v", payload)
	}
	if payload["active_sha256"] != contentSHA(`{"v": 1}`) || payload["last_good_sha256"] != contentSHA(`{"v": 1}`) {
		t.Fatalf("active and last-good hashes must match the applied config, payload: %v", payload)
	}

	// The bootstrap sets the marker when a crash interrupted an apply; the
	// status route must surface it verbatim.
	control.stateAccess.Lock()
	control.interruptedApply = true
	control.stateAccess.Unlock()
	_, payload = adminRequest(t, http.MethodGet, server.URL+"/admin/status", "")
	if payload["interrupted_apply"] != true {
		t.Fatalf("interrupted-apply marker must be surfaced, payload: %v", payload)
	}
}

func TestAdminStatusFatalAfterFailedApply(t *testing.T) {
	service := &fakeReloader{failFor: map[string]error{`{"v": 1}`: E.New("bind failed")}}
	control := newTestController(t, service, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", `{"v": 1}`); status != http.StatusInternalServerError {
		t.Fatal("expected the apply to fail with 500, got", status)
	}
	_, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/status", "")
	if payload["status"] != "fatal" {
		t.Fatalf("a failed apply with no last-good must be fatal, payload: %v", payload)
	}
	if lastError, _ := payload["last_error"].(string); lastError == "" {
		t.Fatalf("the fatal cause must be visible, payload: %v", payload)
	}
}

func TestAdminConfigEndpoint(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	if status, _ := adminRequest(t, http.MethodGet, server.URL+"/admin/config", ""); status != http.StatusNotFound {
		t.Fatal("expected 404 before any apply, got", status)
	}

	content := `{"v": 1, "note": "  exact bytes matter  "}`
	if status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/apply", content); status != http.StatusOK {
		t.Fatal("apply failed with", status)
	}
	response, err := http.Get(server.URL + "/admin/config")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(raw) != content {
		t.Fatalf("config must round-trip byte-exact, got %d %q", response.StatusCode, raw)
	}
}

func TestAdminCertPin(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	registry := newTestRegistry(t)
	control.clients = registry
	handler := control.adminHandler("")

	// No client certificate presented: the pinned plane must refuse.
	request := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	status, payload := serveAdmin(t, handler, request)
	if status != http.StatusUnauthorized {
		t.Fatal("expected 401 without a client cert, got", status)
	}
	if errorText, _ := payload["error"].(string); !strings.Contains(errorText, "client certificate not trusted") {
		t.Fatalf("expected the not-trusted error, payload: %v", payload)
	}

	// A certificate nobody enrolled must be refused too.
	strangerDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := x509.ParseCertificate(strangerDER)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{stranger}}
	if status, _ = serveAdmin(t, handler, request); status != http.StatusUnauthorized {
		t.Fatal("expected 401 for an unenrolled cert, got", status)
	}

	// Enroll a certificate, then present it: the pin must let it through.
	trustedDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := x509.ParseCertificate(trustedDER)
	if err != nil {
		t.Fatal(err)
	}
	code, err := registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.enroll(code, "laptop", trustedDER); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}
	status, payload = serveAdmin(t, handler, request)
	if status != http.StatusOK || payload["status"] != "idle" {
		t.Fatalf("a pinned cert must reach the route, got %d %v", status, payload)
	}
}

func TestAdminEnrollEndpoint(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	registry := newTestRegistry(t)
	control.clients = registry
	handler := control.adminHandler("")

	enroll := func(body []byte) (int, map[string]any) {
		request := httptest.NewRequest(http.MethodPost, "/admin/enroll", bytes.NewReader(body))
		return serveAdmin(t, handler, request)
	}
	certPEM := testClientCertPEM(t)
	certDER, err := decodeCertPEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	validBody := func(code, name string) []byte {
		encoded, _ := json.Marshal(map[string]string{"code": code, "name": name, "cert_pem": string(certPEM)})
		return encoded
	}

	// Malformed JSON and malformed certificate PEM are client errors.
	if status, _ := enroll([]byte("{not json")); status != http.StatusBadRequest {
		t.Fatal("expected 400 for malformed JSON, got", status)
	}
	badPEM, _ := json.Marshal(map[string]string{"code": "x", "cert_pem": "not a pem"})
	if status, _ := enroll(badPEM); status != http.StatusBadRequest {
		t.Fatal("expected 400 for a malformed cert PEM, got", status)
	}

	// No active code minted yet: forbidden.
	if status, _ := enroll(validBody("K7QM-XXNP-2RTD", "self")); status != http.StatusForbidden {
		t.Fatal("expected 403 with no active code, got", status)
	}

	code, err := registry.mintCode("op-label")
	if err != nil {
		t.Fatal(err)
	}

	// A wrong code is forbidden and must not burn the active one.
	if status, _ := enroll(validBody("WRONG-CODE-HERE", "self")); status != http.StatusForbidden {
		t.Fatal("expected 403 for a wrong code, got", status)
	}

	// Valid code: enrolled, and the operator's label wins over the name the
	// client suggests for itself.
	status, payload := enroll(validBody(code, "self"))
	if status != http.StatusOK || payload["enrolled"] != true {
		t.Fatalf("valid enroll must succeed, got %d %v", status, payload)
	}
	if payload["name"] != "op-label" {
		t.Fatalf("operator label must win over the enrollee's name, payload: %v", payload)
	}
	if payload["fingerprint"] != fingerprintOf(certDER) {
		t.Fatalf("fingerprint must match the presented cert, payload: %v", payload)
	}
	if !registry.isTrusted(fingerprintOf(certDER)) {
		t.Fatal("enrolled cert must be pinned in the registry")
	}
}

func TestAdminOperatorRoutes(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	registry := newTestRegistry(t)
	control.clients = registry
	handler := control.adminHandler("s3cret")

	operatorGet := func(remoteAddr, bearer string) (int, map[string]any) {
		request := httptest.NewRequest(http.MethodGet, "/admin/clients", nil)
		request.RemoteAddr = remoteAddr
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		return serveAdmin(t, handler, request)
	}

	// Loopback + secret is the operator contract; no client cert is needed.
	if status, _ := operatorGet("127.0.0.1:12345", "s3cret"); status != http.StatusOK {
		t.Fatal("expected 200 for loopback with bearer, got", status)
	}
	if status, _ := operatorGet("127.0.0.1:12345", ""); status != http.StatusUnauthorized {
		t.Fatal("expected 401 for loopback without bearer, got", status)
	}
	// A remote peer must be refused even with the correct secret: minting a
	// code grants trust and must never be reachable from the network.
	if status, _ := operatorGet("192.0.2.10:443", "s3cret"); status != http.StatusForbidden {
		t.Fatal("expected 403 for a non-loopback peer, got", status)
	}

	// /admin/client-code forwards the operator's label into the mint: redeem
	// the invite and the enrolled client must carry that label.
	request := httptest.NewRequest(http.MethodPost, "/admin/client-code", strings.NewReader(`{"name":"x"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Authorization", "Bearer s3cret")
	status, payload := serveAdmin(t, handler, request)
	if status != http.StatusOK {
		t.Fatal("expected 200 minting a code, got", status)
	}
	invite, _ := payload["invite"].(string)
	parts := strings.Split(invite, "#")
	code := parts[len(parts)-1]
	if code == "" {
		t.Fatalf("invite must end with the code, got %q", invite)
	}
	enrollBody, _ := json.Marshal(map[string]string{"code": code, "name": "self", "cert_pem": string(testClientCertPEM(t))})
	request = httptest.NewRequest(http.MethodPost, "/admin/enroll", bytes.NewReader(enrollBody))
	status, payload = serveAdmin(t, handler, request)
	if status != http.StatusOK || payload["name"] != "x" {
		t.Fatalf("the minted label must reach the enrolled client, got %d %v", status, payload)
	}

	// /admin/client-remove: removing the enrolled client succeeds once, then 404.
	remove := func() (int, map[string]any) {
		request := httptest.NewRequest(http.MethodPost, "/admin/client-remove", strings.NewReader(`{"target":"x"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Authorization", "Bearer s3cret")
		return serveAdmin(t, handler, request)
	}
	status, payload = remove()
	if status != http.StatusOK || payload["removed"] != true {
		t.Fatalf("removing an enrolled client must succeed, got %d %v", status, payload)
	}
	if status, _ = remove(); status != http.StatusNotFound {
		t.Fatal("expected 404 removing an absent client, got", status)
	}
}

// TestAdminTrustedCertNeedsNoBearer pins the SPEC 057 revision: a trusted
// client certificate is the client's FULL credential on the mTLS plane — no
// Bearer on top (clients never learn the daemon-owned secret). And the
// inverse: the Bearer secret alone must NOT open the pinned plane, or a
// leaked operator secret would void the whole mTLS model remotely.
func TestAdminTrustedCertNeedsNoBearer(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	registry := newTestRegistry(t)
	control.clients = registry
	handler := control.adminHandler("s3cret")

	trustedDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := x509.ParseCertificate(trustedDER)
	if err != nil {
		t.Fatal(err)
	}
	code, err := registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.enroll(code, "laptop", trustedDER); err != nil {
		t.Fatal(err)
	}

	// Trusted cert, NO Authorization header: must pass.
	request := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}
	if status, payload := serveAdmin(t, handler, request); status != http.StatusOK {
		t.Fatalf("trusted cert without Bearer must pass, got %d %v", status, payload)
	}

	// Correct Bearer, no cert: must NOT open the pinned plane.
	request = httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	request.Header.Set("Authorization", "Bearer s3cret")
	if status, _ := serveAdmin(t, handler, request); status != http.StatusUnauthorized {
		t.Fatal("Bearer alone must not bypass the cert pin, got", status)
	}
}

func TestAdminInfoEndpoint(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	control.infoStateDir = "/tmp/lxd-info-test/state"
	control.infoTLS = true
	control.serverFingerprint = "abc123"
	control.advertiseAddr = "127.0.0.1:29091"
	control.startedAt = time.Now().Add(-3 * time.Second)
	handler := control.adminHandler("")

	request := httptest.NewRequest(http.MethodGet, "/admin/info", nil)
	status, payload := serveAdmin(t, handler, request)
	if status != http.StatusOK {
		t.Fatal("info must be served, got", status)
	}
	if payload["state_dir"] != "/tmp/lxd-info-test/state" {
		t.Fatalf("state_dir = %v", payload["state_dir"])
	}
	if payload["listen"] != "127.0.0.1:29091" {
		t.Fatalf("listen = %v", payload["listen"])
	}
	if payload["tls"] != true || payload["fingerprint"] != "abc123" {
		t.Fatalf("tls/fingerprint = %v/%v", payload["tls"], payload["fingerprint"])
	}
	if version, _ := payload["version"].(string); version == "" {
		t.Fatal("version must be non-empty")
	}
	if uptime, _ := payload["uptime_seconds"].(float64); uptime < 3 {
		t.Fatalf("uptime_seconds = %v, want >= 3", payload["uptime_seconds"])
	}
	if pid, _ := payload["pid"].(float64); int(pid) != os.Getpid() {
		t.Fatalf("pid = %v, want %d", payload["pid"], os.Getpid())
	}
}
