//go:build with_lxd

package lxd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newResourceTestController wires a controller with a real resource store over
// a temp dir, plus the fake reloader so apply/guard interplay is exercisable.
// infoResourcesDir is set to the same absolute dir the store writes to, so
// handler-stamped `path` values are assertable.
func newResourceTestController(t *testing.T) (*controller, string) {
	t.Helper()
	stateDir := t.TempDir()
	stateStore, err := newStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	resDir := filepath.Join(stateDir, "resources")
	resStore, err := newResourceStore(resDir)
	if err != nil {
		t.Fatal(err)
	}
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	control := &controller{
		service:          &fakeReloader{},
		store:            stateStore,
		resources:        resStore,
		validate:         func(ctx context.Context, configPath string) error { return nil },
		infoStateDir:     absStateDir,
		infoResourcesDir: filepath.Join(absStateDir, "resources"),
	}
	return control, resDir
}

func TestResourceStorePutStatReadDelete(t *testing.T) {
	control, resDir := newResourceTestController(t)
	body := []byte("srs-binary-bytes")

	sha, size, err := control.resources.Put("geosite-ru.srs", body)
	if err != nil {
		t.Fatal(err)
	}
	if size != len(body) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
	if sha != contentSHA(string(body)) {
		t.Fatal("sha mismatch")
	}
	// File actually lands in the resources dir under its exact name.
	if _, statErr := os.Stat(filepath.Join(resDir, "geosite-ru.srs")); statErr != nil {
		t.Fatal("resource file not on disk:", statErr)
	}

	meta, found, err := control.resources.Stat("geosite-ru.srs")
	if err != nil || !found {
		t.Fatal("stat: found=false or err:", err)
	}
	if meta.SHA256 != sha || meta.Size != size {
		t.Fatal("stat metadata mismatch")
	}

	got, found, err := control.resources.Read("geosite-ru.srs")
	if err != nil || !found {
		t.Fatal("read: found=false or err:", err)
	}
	if string(got) != string(body) {
		t.Fatal("read content mismatch")
	}

	removed, err := control.resources.Delete("geosite-ru.srs")
	if err != nil || !removed {
		t.Fatal("delete: removed=false or err:", err)
	}
	if _, found, _ = control.resources.Stat("geosite-ru.srs"); found {
		t.Fatal("resource still present after delete")
	}
}

func TestResourceStoreOverwriteUpdatesHash(t *testing.T) {
	control, _ := newResourceTestController(t)
	if _, _, err := control.resources.Put("r.srs", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	sha2, _, err := control.resources.Put("r.srs", []byte("v2-longer"))
	if err != nil {
		t.Fatal(err)
	}
	meta, _, _ := control.resources.Stat("r.srs")
	if meta.SHA256 != sha2 || meta.SHA256 != contentSHA("v2-longer") {
		t.Fatal("overwrite did not update hash")
	}
}

func TestResourceStoreListSortedSkipsTempAndMissing(t *testing.T) {
	control, resDir := newResourceTestController(t)
	for _, n := range []string{"b.srs", "a.srs"} {
		if _, _, err := control.resources.Put(n, []byte(n)); err != nil {
			t.Fatal(err)
		}
	}
	// A leftover temp file (writeAtomic prefix) must never appear as a resource.
	if err := os.WriteFile(filepath.Join(resDir, ".tmp-leftover"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, err := control.resources.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].Name != "a.srs" || metas[1].Name != "b.srs" {
		t.Fatalf("list not sorted/filtered: %+v", metas)
	}
}

func TestResourceNameSanitization(t *testing.T) {
	bad := []string{"", ".", "..", "../evil", "a/b", "a\\b", ".hidden", "/abs"}
	for _, name := range bad {
		if _, err := sanitizeResourceName(name); err == nil {
			t.Fatalf("name %q should be rejected", name)
		} else if !isBadResourceName(err) {
			t.Fatalf("name %q rejected but not as badResourceName", name)
		}
	}
	for _, name := range []string{"geosite-ru.srs", "geoip.db", "a_b-1.2.srs"} {
		if _, err := sanitizeResourceName(name); err != nil {
			t.Fatalf("name %q should be accepted: %v", name, err)
		}
	}
}

// --- reference guard (B2) --------------------------------------------------

func TestResourceReferencedByActiveConfig(t *testing.T) {
	control, _ := newResourceTestController(t)
	control.activeContent = `{"rule_set":[{"type":"local","path":"/x/resources/geosite-ru.srs"}]}`
	ref, err := control.resourceReferenced("geosite-ru.srs")
	if err != nil {
		t.Fatal(err)
	}
	if !ref {
		t.Fatal("expected referenced=true for active config")
	}
	// A free name is not referenced.
	if ref, _ = control.resourceReferenced("unused.srs"); ref {
		t.Fatal("free name reported as referenced")
	}
}

func TestResourceReferencedByLastGood(t *testing.T) {
	control, _ := newResourceTestController(t)
	if err := control.store.SaveLastGood(`{"path":"/x/resources/geoip.db"}`); err != nil {
		t.Fatal(err)
	}
	ref, err := control.resourceReferenced("geoip.db")
	if err != nil {
		t.Fatal(err)
	}
	if !ref {
		t.Fatal("expected referenced=true for last-good")
	}
}

// --- HTTP plane ------------------------------------------------------------

// resourceServer builds the admin handler in dev mode (no clients => Bearer
// gate with empty secret => open), so requests need no auth wiring.
func resourceServer(t *testing.T, control *controller) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(control.adminHandler(""))
	t.Cleanup(srv.Close)
	return srv
}

func TestResourcePutThenStatThenContent(t *testing.T) {
	control, _ := newResourceTestController(t)
	srv := resourceServer(t, control)
	body := "compiled-rule-set"

	// PUT
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/geosite-ru.srs", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}
	var putMeta resourceMeta
	_ = json.NewDecoder(resp.Body).Decode(&putMeta)
	resp.Body.Close()
	wantPath := filepath.Join(control.infoResourcesDir, "geosite-ru.srs")
	if putMeta.Path != wantPath {
		t.Fatalf("PUT path = %q, want absolute %q", putMeta.Path, wantPath)
	}
	if !filepath.IsAbs(putMeta.Path) {
		t.Fatal("PUT path is not absolute")
	}
	if putMeta.SHA256 != contentSHA(body) {
		t.Fatal("PUT sha mismatch")
	}

	// GET metadata
	resp, err = http.Get(srv.URL + "/admin/resources/geosite-ru.srs")
	if err != nil {
		t.Fatal(err)
	}
	var statMeta resourceMeta
	_ = json.NewDecoder(resp.Body).Decode(&statMeta)
	resp.Body.Close()
	if statMeta.SHA256 != putMeta.SHA256 || statMeta.Path != wantPath {
		t.Fatal("GET metadata mismatch")
	}

	// GET content
	resp, err = http.Get(srv.URL + "/admin/resources/geosite-ru.srs/content")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != body {
		t.Fatal("GET content mismatch")
	}
}

func TestResourceListEndpoint(t *testing.T) {
	control, _ := newResourceTestController(t)
	srv := resourceServer(t, control)
	for _, n := range []string{"a.srs", "b.srs"} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/"+n, strings.NewReader(n))
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
	}
	resp, err := http.Get(srv.URL + "/admin/resources")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Resources []resourceMeta `json:"resources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	resp.Body.Close()
	if len(payload.Resources) != 2 {
		t.Fatalf("list len = %d", len(payload.Resources))
	}
	if !filepath.IsAbs(payload.Resources[0].Path) {
		t.Fatal("list path not absolute")
	}
}

func TestResourcePutRejectsBadName(t *testing.T) {
	control, _ := newResourceTestController(t)
	srv := resourceServer(t, control)
	// A traversal name in the last path segment reaches the handler decoded.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/..%2Fevil", strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad name status = %d, want 400", resp.StatusCode)
	}
	// And nothing escaped the resources dir.
	if _, statErr := os.Stat(filepath.Join(control.infoStateDir, "evil")); statErr == nil {
		t.Fatal("traversal wrote outside resources/")
	}
}

func TestResourcePutConflictWhenReferenced(t *testing.T) {
	control, _ := newResourceTestController(t)
	control.activeContent = `{"path":"/x/resources/geosite-ru.srs"}`
	srv := resourceServer(t, control)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/geosite-ru.srs", strings.NewReader("new"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT referenced status = %d, want 409", resp.StatusCode)
	}
}

func TestResourceDeleteConflictWhenReferenced(t *testing.T) {
	control, _ := newResourceTestController(t)
	if _, _, err := control.resources.Put("geoip.db", []byte("x")); err != nil {
		t.Fatal(err)
	}
	control.activeContent = `{"path":"/x/resources/geoip.db"}`
	srv := resourceServer(t, control)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/resources/geoip.db", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE referenced status = %d, want 409", resp.StatusCode)
	}
	// The file must still be there — a blocked delete leaves state intact.
	if _, found, _ := control.resources.Stat("geoip.db"); !found {
		t.Fatal("blocked delete removed the file")
	}
}

func TestResourceDeleteFreeNameSucceeds(t *testing.T) {
	control, _ := newResourceTestController(t)
	if _, _, err := control.resources.Put("free.srs", []byte("x")); err != nil {
		t.Fatal(err)
	}
	srv := resourceServer(t, control)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/resources/free.srs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE free status = %d, want 200", resp.StatusCode)
	}
}

func TestResourceStatNotFound(t *testing.T) {
	control, _ := newResourceTestController(t)
	srv := resourceServer(t, control)
	resp, err := http.Get(srv.URL + "/admin/resources/missing.srs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing stat status = %d, want 404", resp.StatusCode)
	}
}
