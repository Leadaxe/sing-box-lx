//go:build with_lxd

package lxd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"
)

func TestResourceStoreKeepsOldestPrevious(t *testing.T) {
	store, err := newResourceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, kept, _ := store.PutKeepingPrevious("a.srs", []byte("v1")); kept {
		t.Fatal("nothing to keep on the first write")
	}
	if _, _, kept, _ := store.PutKeepingPrevious("a.srs", []byte("v2")); !kept {
		t.Fatal("v1 not kept")
	}
	// A second unconfirmed write must not replace the kept copy with v2.
	if _, _, kept, _ := store.PutKeepingPrevious("a.srs", []byte("v3")); !kept {
		t.Fatal("kept copy lost")
	}
	if metas, _ := store.List(); len(metas) != 1 || metas[0].Name != "a.srs" {
		t.Fatalf("kept copy leaked into the listing: %+v", metas)
	}
	restored, err := store.RestoreAllPrevious()
	if err != nil || len(restored) != 1 {
		t.Fatalf("restore: %v %v", restored, err)
	}
	if content, _, _ := store.Read("a.srs"); string(content) != "v1" {
		t.Fatalf("restored %q, want v1", content)
	}
	if again, _ := store.RestoreAllPrevious(); len(again) != 0 {
		t.Fatal("restore is not one-shot")
	}
}

func TestCheckRuleSetContentBuildsRules(t *testing.T) {
	// Decodes, but the rule cannot be built: the core would refuse it too.
	source := `{"version":1,"rules":[{"domain_regex":["("]}]}`
	if checkable, err := checkRuleSetContent("bad.json", []byte(source)); !checkable || err == nil {
		t.Fatalf("unbuildable rule accepted: checkable=%v err=%v", checkable, err)
	}
}

const refConfig = `{"route":{"rule_set":[{"type":"local","tag":"ru","format":"binary","path":%PATH%}]}}`

// refConfigFor points refConfig at path, JSON-escaped as a real config has it
// (a Windows path carries backslashes).
func refConfigFor(t *testing.T, path string) string {
	t.Helper()
	quoted, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(refConfig, "%PATH%", string(quoted))
}

// stoppedReplace uploads v2 over v1 while only last-good points at the file,
// and returns the controller with the replacement pending.
func stoppedReplace(t *testing.T, service reloader) (control *controller, config string, v1 []byte, v2 []byte) {
	t.Helper()
	control, resDir := newResourceTestController(t)
	control.service = service
	v1, v2 = testRuleSetBinary(t, "old.example"), testRuleSetBinary(t, "new.example")
	if _, _, err := control.resources.Put("ru.srs", v1); err != nil {
		t.Fatal(err)
	}
	config = refConfigFor(t, filepath.Join(resDir, "ru.srs"))
	if err := control.store.SaveLastGood(config); err != nil {
		t.Fatal(err)
	}
	srv := resourceServer(t, control)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/ru.srs", bytes.NewReader(v2))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body["replace"] != replacePending {
		t.Fatalf("PUT while stopped: status %d body %v, want 200 pending", resp.StatusCode, body)
	}
	return control, config, v1, v2
}

func resourceBytes(t *testing.T, control *controller, name string) []byte {
	t.Helper()
	content, _, err := control.resources.Read(name)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func keptCount(t *testing.T, control *controller) int {
	t.Helper()
	names, err := control.resources.keptNames()
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

// A start that succeeds confirms the pending replacement.
func TestPendingReplaceConfirmedByStart(t *testing.T) {
	control, _, _, v2 := stoppedReplace(t, &fakeReloader{})
	if keptCount(t, control) != 1 {
		t.Fatal("previous copy not kept while pending")
	}
	if result, found := control.Start(context.Background()); !found || result.Outcome != applyApplied {
		t.Fatalf("start: %+v", result)
	}
	if keptCount(t, control) != 0 {
		t.Fatal("previous copy survived a successful start")
	}
	if !bytes.Equal(resourceBytes(t, control, "ru.srs"), v2) {
		t.Fatal("confirmed replacement lost")
	}
}

// flakyReloader fails the first `failures` starts, recording which bytes of
// the watched file each attempt saw.
type flakyReloader struct {
	failures int
	path     string
	seen     [][]byte
}

func (f *flakyReloader) StartOrReloadService(ctx context.Context, content string, options *daemon.OverrideOptions) error {
	current, _ := os.ReadFile(f.path)
	f.seen = append(f.seen, current)
	if f.failures > 0 {
		f.failures--
		return E.New("rule-set refused")
	}
	return nil
}

func (f *flakyReloader) CloseService() error { return nil }

// A start that fails over an unconfirmed set puts the previous bytes back and
// tries once more — last-good never stays on a set it cannot start with.
func TestPendingReplaceRevertedByFailedStart(t *testing.T) {
	service := &flakyReloader{failures: 1}
	control, _, v1, v2 := stoppedReplace(t, service)
	service.path = control.resources.pathOf("ru.srs")

	result, found := control.Start(context.Background())
	if !found || result.Outcome != applyApplied {
		t.Fatalf("start after revert: %+v", result)
	}
	if len(service.seen) != 2 || !bytes.Equal(service.seen[0], v2) || !bytes.Equal(service.seen[1], v1) {
		t.Fatalf("attempts did not go new-then-previous (%d attempts)", len(service.seen))
	}
	if !bytes.Equal(resourceBytes(t, control, "ru.srs"), v1) || keptCount(t, control) != 0 {
		t.Fatal("previous bytes not restored")
	}
}

// The same on apply, including the case the pipeline used to call fatal: the
// applied config IS last-good, and only the resource differs.
func TestApplyRevertsPendingReplaceAndFallsBack(t *testing.T) {
	service := &flakyReloader{failures: 1}
	control, config, v1, _ := stoppedReplace(t, service)
	service.path = control.resources.pathOf("ru.srs")

	result := control.Apply(context.Background(), config)
	if result.Outcome != applyFailed || !result.RolledBack {
		t.Fatalf("apply: %+v, want failed + rolled back", result)
	}
	if len(service.seen) != 2 || !bytes.Equal(service.seen[1], v1) {
		t.Fatal("fallback did not run over the previous bytes")
	}
	if !bytes.Equal(resourceBytes(t, control, "ru.srs"), v1) {
		t.Fatal("previous bytes not restored")
	}
}

// Boot takes the same path.
func TestBootRevertsPendingReplace(t *testing.T) {
	service := &flakyReloader{failures: 1}
	control, _, v1, _ := stoppedReplace(t, service)
	service.path = control.resources.pathOf("ru.srs")

	control.applyAccess.Lock()
	bootstrap(context.Background(), control, control.store, Options{Run: true})
	control.applyAccess.Unlock()
	if !control.running {
		t.Fatalf("boot did not recover: %q", control.lastError)
	}
	if !bytes.Equal(resourceBytes(t, control, "ru.srs"), v1) {
		t.Fatal("previous bytes not restored")
	}
}

// liveReloader is a core that is up and exposes real local rule-sets, each
// watching its file as the core's own do.
type liveReloader struct {
	fakeReloader
	sets map[string]*rule.LocalRuleSet
}

func (l *liveReloader) LiveRuleSet(tag string) (liveRuleSet, bool) {
	ruleSet, loaded := l.sets[tag]
	return ruleSet, loaded
}

// refuseAfterFirst lets the initial load through and refuses every reload.
type refuseAfterFirst struct{ calls atomic.Int32 }

func (r *refuseAfterFirst) ValidateRuleSetMetadataUpdate(tag string, metadata adapter.RuleSetMetadata) error {
	if r.calls.Add(1) == 1 {
		return nil
	}
	return E.New("dns rule refuses this rule-set")
}

func matchesDomain(ruleSet *rule.LocalRuleSet, domain string) bool {
	return ruleSet.Match(&adapter.InboundContext{Domain: domain})
}

// runningReplace brings up a controller whose running core loads ru.srs
// through a real LocalRuleSet, then PUTs v2 over it.
func runningReplace(t *testing.T, ctx context.Context) (control *controller, live *rule.LocalRuleSet, status int, body map[string]any) {
	t.Helper()
	control, resDir := newResourceTestController(t)
	control.ruleSetConfirmTimeout = 2 * time.Second
	path := filepath.Join(resDir, "ru.srs")
	if _, _, err := control.resources.Put("ru.srs", testRuleSetBinary(t, "old.example")); err != nil {
		t.Fatal(err)
	}
	live, err := rule.NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "ru", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Tag:          badoption.Listable[string]{"ru"},
		Format:       C.RuleSetFormatBinary,
		LocalOptions: option.LocalRuleSet{Path: path},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Close() })
	if err = live.StartContext(ctx, nil); err != nil {
		t.Fatal(err)
	}
	control.service = &liveReloader{sets: map[string]*rule.LocalRuleSet{"ru": live}}
	control.setActive(refConfigFor(t, path), "")

	srv := resourceServer(t, control)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/admin/resources/ru.srs", bytes.NewReader(testRuleSetBinary(t, "new.example")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	return control, live, resp.StatusCode, body
}

// The running core reloads the replaced file on its own; the PUT waits for
// that and reports it.
func TestReplaceConfirmedByRunningCore(t *testing.T) {
	control, live, status, body := runningReplace(t, context.Background())
	if status != http.StatusOK || body["replace"] != replaceConfirmed {
		t.Fatalf("PUT: status %d body %v, want 200 confirmed", status, body)
	}
	if !matchesDomain(live, "a.new.example") || matchesDomain(live, "a.old.example") {
		t.Fatal("running rule-set still holds the old rules")
	}
	if keptCount(t, control) != 0 {
		t.Fatal("previous copy kept after the core confirmed")
	}
}

// A set the running core refuses is rolled back at once: disk and memory both
// stay on the previous version.
func TestReplaceRefusedByRunningCore(t *testing.T) {
	ctx := service.ContextWith[adapter.DNSRuleSetUpdateValidator](context.Background(), &refuseAfterFirst{})
	control, live, status, body := runningReplace(t, ctx)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT: status %d body %v, want 422", status, body)
	}
	if !bytes.Equal(resourceBytes(t, control, "ru.srs"), testRuleSetBinary(t, "old.example")) {
		t.Fatal("refused rule-set left on disk")
	}
	if !matchesDomain(live, "a.old.example") || matchesDomain(live, "a.new.example") || keptCount(t, control) != 0 {
		t.Fatal("running rule-set or kept copy in the wrong state")
	}
}

// Re-uploading the bytes already on disk changes nothing and proves nothing.
func TestReplaceSameBytesIsNoop(t *testing.T) {
	control, _, _, _ := stoppedReplace(t, &fakeReloader{})
	_, outcome, rejected, err := control.replaceReferenced("ru.srs", resourceBytes(t, control, "ru.srs"))
	if err != nil || rejected != nil || outcome != "" {
		t.Fatalf("same bytes: outcome=%q rejected=%v err=%v", outcome, rejected, err)
	}
	if keptCount(t, control) != 1 {
		t.Fatal("pending copy must survive a no-op upload")
	}
}
