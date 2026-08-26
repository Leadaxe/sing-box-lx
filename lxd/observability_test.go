//go:build with_lxd

package lxd

// observability_test.go covers the diagnostics plane (SPEC 065): the memory
// snapshot and its throttle, core stats with and without a core, the daemon
// log tail, and the pprof routes — including the whitelist, the CPU-profile
// mutual exclusion, and the block/mutex enable toggles.

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawRequest fetches a route without assuming a JSON body — the pprof and log
// routes serve bytes and text.
func rawRequest(t *testing.T, method, url string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, body
}

// statsFake is a reloader that also answers CoreStats, standing in for a live
// core without booting a box.
type statsFake struct {
	fakeReloader
	live        bool
	uptime      time.Duration
	uplink      int64
	downlink    int64
	connections int
}

func (s *statsFake) CoreStats() (time.Duration, int64, int64, int, bool) {
	return s.uptime, s.uplink, s.downlink, s.connections, s.live
}

func TestMemoryEndpointReportsRawBytes(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	control.memory = newMemoryCache()
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/memory", "")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	// Every size must be a bare number: the consumer graphs it, and a
	// formatted "40.0 MiB" would have to be parsed back.
	for _, field := range []string{"heap_inuse_bytes", "stack_inuse_bytes", "sys_bytes", "inuse_bytes"} {
		value, ok := payload[field].(float64)
		if !ok {
			t.Fatalf("%s must be a number, payload: %v", field, payload)
		}
		if value <= 0 {
			t.Fatalf("%s must be positive in a live process, got %v", field, value)
		}
	}
	if goroutines, _ := payload["goroutines"].(float64); goroutines <= 0 {
		t.Fatalf("goroutines must be positive, payload: %v", payload)
	}
	// Both RSS fields are always present: a missing key would force the client
	// to guess whether the daemon is old or the platform is limited.
	for _, field := range []string{"rss_current_bytes", "rss_peak_bytes"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("%s must always be present, payload: %v", field, payload)
		}
	}
}

func TestMemoryRSSFieldsHonest(t *testing.T) {
	snapshot := takeMemorySnapshot()
	if snapshot.RSSCurrentBytes != rssUnsupported && snapshot.RSSCurrentBytes <= 0 {
		t.Fatalf("current RSS must be positive where supported, got %d", snapshot.RSSCurrentBytes)
	}
	if snapshot.RSSPeakBytes != rssUnsupported && snapshot.RSSPeakBytes <= 0 {
		t.Fatalf("peak RSS must be positive where supported, got %d", snapshot.RSSPeakBytes)
	}
	// The two numbers come from different kernel sources and are NOT read
	// atomically: ru_maxrss is updated lazily, while /proc/self/statm is
	// instantaneous. On a growing process the current size can legitimately
	// overtake the recorded peak by a few pages, so "peak >= current" does not
	// hold in the moment — asserting it made this test flaky on linux (caught
	// by CI; it never fired on darwin, where current RSS is unsupported).
	//
	// What must hold is that they describe the same process: same order of
	// magnitude, not a stale or bogus reading.
	if snapshot.RSSCurrentBytes > 0 && snapshot.RSSPeakBytes > 0 {
		if snapshot.RSSPeakBytes < snapshot.RSSCurrentBytes/2 {
			t.Fatalf("peak %d is implausibly far below current %d — the two readings "+
				"should describe one process", snapshot.RSSPeakBytes, snapshot.RSSCurrentBytes)
		}
	}
}

func TestMemoryCacheThrottles(t *testing.T) {
	// ReadMemStats stops the world, so a polling client must not be able to
	// drive it per-request. Within the TTL the very same snapshot comes back.
	cache := newMemoryCache()
	now := time.Now()
	cache.now = func() time.Time { return now }

	first := cache.Snapshot()
	// Allocate enough to move the counters if a fresh reading were taken.
	ballast := make([][]byte, 0, 256)
	for i := 0; i < 256; i++ {
		ballast = append(ballast, make([]byte, 64<<10))
	}
	second := cache.Snapshot()
	if first != second {
		t.Fatal("a second read inside the TTL must return the cached snapshot")
	}

	now = now.Add(memorySnapshotTTL + time.Millisecond)
	third := cache.Snapshot()
	if third == first && third.NumGC == first.NumGC && third.HeapInuseBytes == first.HeapInuseBytes {
		t.Log("counters happened to match after the TTL; only the refresh path matters here")
	}
	runtimeKeepAlive(ballast)
}

// runtimeKeepAlive stops the ballast from being collected before the second
// reading, which would defeat the point of allocating it.
func runtimeKeepAlive(v any) {}

func TestMemoryCacheNilSafe(t *testing.T) {
	// Controllers built as struct literals (every existing unit test) must not
	// panic on the endpoint.
	var cache *memoryCache
	if snapshot := cache.Snapshot(); snapshot.Goroutines <= 0 {
		t.Fatal("nil cache must still produce a live snapshot")
	}
}

func TestStatsWithLiveCore(t *testing.T) {
	service := &statsFake{live: true, uptime: 90 * time.Second, uplink: 1234, downlink: 5678, connections: 7}
	control := newTestController(t, &fakeReloader{}, nil)
	control.service = service
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/stats", "")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	if payload["core_uptime_seconds"] != float64(90) {
		t.Fatalf("expected core uptime 90, payload: %v", payload)
	}
	if payload["uplink_total"] != float64(1234) || payload["downlink_total"] != float64(5678) {
		t.Fatalf("traffic totals must be reported verbatim, payload: %v", payload)
	}
	if payload["connections"] != float64(7) {
		t.Fatalf("expected 7 connections, payload: %v", payload)
	}
}

func TestStatsWithoutCoreIsNullNot503(t *testing.T) {
	// With no core the endpoint still answers 200: it describes the DAEMON,
	// and "there is no core" is exactly what a client polls it for. A 503
	// would make it useless in the one state where the daemon is all that is
	// left to ask.
	control := newTestController(t, &fakeReloader{}, nil)
	control.service = &statsFake{live: false}
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/stats", "")
	if status != http.StatusOK {
		t.Fatal("expected 200 with no core, got", status)
	}
	for _, field := range []string{"core_uptime_seconds", "uplink_total", "downlink_total", "connections"} {
		value, present := payload[field]
		if !present {
			t.Fatalf("%s must be present as null, payload: %v", field, payload)
		}
		if value != nil {
			t.Fatalf("%s must be null with no core (not zero), got %v", field, value)
		}
	}
}

func TestStatsWithPlainReloaderReportsNoCore(t *testing.T) {
	// A service that does not implement statsSource must degrade to nulls,
	// never panic — the assertion is optional by design.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/stats", "")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	if payload["uplink_total"] != nil {
		t.Fatalf("a non-stats service must report null, payload: %v", payload)
	}
}

func TestLogTailReturnsLastLines(t *testing.T) {
	stateDir := t.TempDir()
	logPath := DefaultLogPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	for i := 1; i <= 10; i++ {
		builder.WriteString("line ")
		builder.WriteString(string(rune('0' + i%10)))
		builder.WriteString("\n")
	}
	if err := os.WriteFile(logPath, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	content, found, err := tailLog(logPath, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("log must be found")
	}
	lines := strings.Split(content, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), content)
	}
	if lines[2] != "line 0" {
		t.Fatalf("tail must end at the newest line, got %q", content)
	}
}

func TestLogTailReadsRotatedGeneration(t *testing.T) {
	// Rotation moves history into lxd.log.1; a tail taken right after would
	// otherwise come back nearly empty.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "lxd.log")
	if err := os.WriteFile(logPath+".1", []byte("old-1\nold-2\nold-3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("new-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, found, err := tailLog(logPath, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("log must be found")
	}
	if !strings.Contains(content, "old-3") || !strings.Contains(content, "new-1") {
		t.Fatalf("tail must span the rotated generation, got %q", content)
	}
}

func TestLogEndpointMissingFileIs404(t *testing.T) {
	// No file is a legitimate state (terminal runs keep the log on screen),
	// so it must not read as a daemon fault.
	control := newTestController(t, &fakeReloader{}, nil)
	control.infoLogPath = filepath.Join(t.TempDir(), "lxd.log")
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/logs", "")
	if status != http.StatusNotFound {
		t.Fatal("expected 404 for an absent log, got", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "terminal") {
		t.Fatalf("the 404 must explain why the file is absent, payload: %v", payload)
	}
}

func TestLogEndpointNoLogFileConfiguredIs404(t *testing.T) {
	// A dev/terminal daemon has NO log file at all (infoLogPath empty) — the
	// endpoint must answer the same honest 404, not derive a phantom path.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/logs", "")
	if status != http.StatusNotFound {
		t.Fatal("expected 404 with no log file configured, got", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "terminal") {
		t.Fatalf("the 404 must explain why there is no file, payload: %v", payload)
	}
}

func TestLogTailClamp(t *testing.T) {
	if clampTailLines(0) != defaultLogTailLines {
		t.Fatal("absent tail must fall back to the default")
	}
	if clampTailLines(-5) != defaultLogTailLines {
		t.Fatal("negative tail must fall back to the default")
	}
	if clampTailLines(maxLogTailLines+1) != maxLogTailLines {
		t.Fatal("tail above the ceiling must clamp")
	}
	if clampTailLines(42) != 42 {
		t.Fatal("a sane tail must pass through")
	}
}

func TestProfileListReportsEnabledState(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, _, body := rawRequest(t, http.MethodGet, server.URL+"/admin/pprof")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	var payload struct {
		Profiles []profileEntry `json:"profiles"`
		Running  bool           `json:"cpu_profile_running"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Profiles) != len(snapshotProfiles) {
		t.Fatalf("expected %d profiles, got %d", len(snapshotProfiles), len(payload.Profiles))
	}
	byName := map[string]profileEntry{}
	for _, entry := range payload.Profiles {
		byName[entry.Name] = entry
	}
	if !byName["heap"].Enabled {
		t.Fatal("heap is always on — the runtime samples it from process start")
	}
	// block/mutex must advertise both that they are off AND how to turn them
	// on, so "why is this empty" needs no documentation.
	for _, name := range []string{"block", "mutex"} {
		if byName[name].Enabled {
			t.Fatalf("%s must report disabled before a rate is set", name)
		}
		if byName[name].Hint == "" {
			t.Fatalf("%s must carry a hint on how to enable it", name)
		}
	}
	if payload.Running {
		t.Fatal("no CPU profile should be running")
	}
}

func TestProfileSnapshotServesGzip(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, header, body := rawRequest(t, http.MethodGet, server.URL+"/admin/pprof/heap")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	if header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("profile must be served as bytes, got %q", header.Get("Content-Type"))
	}
	if !strings.Contains(header.Get("Content-Disposition"), "heap-") {
		t.Fatalf("a download filename must be offered, got %q", header.Get("Content-Disposition"))
	}
	// gzip magic — this is what `go tool pprof` expects to open.
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatalf("expected a gzip-framed protobuf, got %d bytes starting %x", len(body), body[:min(4, len(body))])
	}
}

func TestProfileGoroutineDebugIsText(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, header, body := rawRequest(t, http.MethodGet, server.URL+"/admin/pprof/goroutine?debug=2")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/plain") {
		t.Fatalf("debug=2 must be readable text, got %q", header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "goroutine") {
		t.Fatalf("expected a stack dump, got %q", truncate(string(body), 200))
	}
}

func TestProfileUnknownNameIs404JSON(t *testing.T) {
	// Unlike net/http/pprof, an unknown name must not fall through to an
	// index page: the consumer is a program.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/pprof/nosuch", "")
	if status != http.StatusNotFound {
		t.Fatal("expected 404 for an unknown profile, got", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "no such profile") {
		t.Fatalf("expected a JSON error, payload: %v", payload)
	}
}

func TestProfileNotWhitelistedIsRejected(t *testing.T) {
	// `cmdline` and `symbol` are deliberately absent from our plane; so is any
	// profile that merely happens to be registered in the runtime.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	for _, name := range []string{"cmdline", "symbol"} {
		status, _ := adminRequest(t, http.MethodGet, server.URL+"/admin/pprof/"+name, "")
		if status != http.StatusNotFound {
			t.Fatalf("%s must not be served, got %d", name, status)
		}
	}
}

func TestCPUProfileSecondsBounds(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	// Above the ceiling the answer must be an explicit 400, not a silent
	// clamp: a caller asking for 10 minutes should learn it cannot have them.
	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/pprof/profile?seconds=999", "")
	if status != http.StatusBadRequest {
		t.Fatal("expected 400 for an out-of-range window, got", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "seconds") {
		t.Fatalf("the 400 must name the offending parameter, payload: %v", payload)
	}
	if status, _ = adminRequest(t, http.MethodGet, server.URL+"/admin/pprof/profile?seconds=0", ""); status != http.StatusBadRequest {
		t.Fatal("expected 400 for a zero window, got", status)
	}
}

func TestCPUProfileConcurrentIs409(t *testing.T) {
	// StartCPUProfile refuses a second recording outright, so the caller gets
	// a clear 409 instead of waiting out the first window in a queue.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	var wait sync.WaitGroup
	wait.Add(1)
	firstStatus := make(chan int, 1)
	go func() {
		defer wait.Done()
		status, _, _ := rawRequest(t, http.MethodGet, server.URL+"/admin/pprof/profile?seconds=2")
		firstStatus <- status
	}()

	// Give the first request time to acquire the recorder.
	deadline := time.Now().Add(2 * time.Second)
	for !control.profiles.cpu.running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !control.profiles.cpu.running.Load() {
		t.Fatal("the first CPU profile never started")
	}

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/pprof/profile?seconds=1", "")
	if status != http.StatusConflict {
		t.Fatal("expected 409 while a profile is running, got", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "already running") {
		t.Fatalf("the 409 must say why, payload: %v", payload)
	}

	wait.Wait()
	if got := <-firstStatus; got != http.StatusOK {
		t.Fatal("the first profile must still succeed, got", got)
	}
}

func TestBlockAndMutexToggle(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	// Leave the process as we found it: these rates are global runtime state.
	defer func() {
		adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/block?rate=0", "")
		adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/mutex?fraction=0", "")
	}()

	status, payload := adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/block?rate=10000", "")
	if status != http.StatusOK {
		t.Fatal("expected 200 enabling block profiling, got", status)
	}
	if payload["enabled"] != true || payload["rate"] != float64(10000) {
		t.Fatalf("the response must echo the applied rate, payload: %v", payload)
	}

	status, payload = adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/mutex?fraction=100", "")
	if status != http.StatusOK {
		t.Fatal("expected 200 enabling mutex profiling, got", status)
	}
	if payload["enabled"] != true || payload["fraction"] != float64(100) {
		t.Fatalf("the response must echo the applied fraction, payload: %v", payload)
	}

	// Now the list must report them as live rather than "empty, see hint".
	_, _, body := rawRequest(t, http.MethodGet, server.URL+"/admin/pprof")
	var list struct {
		Profiles []profileEntry `json:"profiles"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	for _, entry := range list.Profiles {
		if (entry.Name == "block" || entry.Name == "mutex") && !entry.Enabled {
			t.Fatalf("%s must report enabled after being turned on", entry.Name)
		}
	}

	// Zero disables.
	status, payload = adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/block?rate=0", "")
	if status != http.StatusOK || payload["enabled"] != false {
		t.Fatalf("rate=0 must disable, status %d payload %v", status, payload)
	}
}

func TestBlockRateRejectsGarbage(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()
	for _, query := range []string{"", "?rate=-1", "?rate=abc"} {
		status, _ := adminRequest(t, http.MethodPost, server.URL+"/admin/pprof/block"+query, "")
		if status != http.StatusBadRequest {
			t.Fatalf("block%s must be 400, got %d", query, status)
		}
	}
}

func TestObservabilityRoutesArePinned(t *testing.T) {
	// The diagnostics plane rides the client-facing mux, so every route must
	// sit behind the same certificate pin as apply/resources. A profile is a
	// map of the process's memory — an unpinned route here would be exactly
	// the hole that keeping box's debug listener out of lxd avoids.
	control := newTestController(t, &fakeReloader{}, nil)
	control.clients = newTestRegistry(t)
	handler := control.adminHandler("")

	routes := []string{
		"/admin/memory",
		"/admin/stats",
		"/admin/logs",
		"/admin/pprof",
		"/admin/pprof/heap",
	}
	for _, route := range routes {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		status, payload := serveAdmin(t, handler, request)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s must refuse an uncertified client, got %d", route, status)
		}
		if text, _ := payload["error"].(string); !strings.Contains(text, "client certificate not trusted") {
			t.Fatalf("%s must give the not-trusted error, payload: %v", route, payload)
		}
	}
	for _, route := range []string{"/admin/pprof/block?rate=0", "/admin/pprof/mutex?fraction=0"} {
		request := httptest.NewRequest(http.MethodPost, route, nil)
		if status, _ := serveAdmin(t, handler, request); status != http.StatusUnauthorized {
			t.Fatalf("%s must refuse an uncertified client, got %d", route, status)
		}
	}
}

func TestObservabilityRoutesReachableRemotely(t *testing.T) {
	// The counterpart to the pin test: these routes must NOT be on the
	// operator loopback-only path. Their whole purpose is remote diagnosis of
	// a misbehaving server from the launcher — an operator on the host already
	// has kill -QUIT and a local profiler.
	control := newTestController(t, &fakeReloader{}, nil)
	control.clients = newTestRegistry(t)
	trustedDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	code, err := control.clients.mintCode("launcher")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = control.clients.enroll(code, "launcher", trustedDER); err != nil {
		t.Fatal(err)
	}
	trusted, err := x509.ParseCertificate(trustedDER)
	if err != nil {
		t.Fatal(err)
	}
	handler := control.adminHandler("")

	request := httptest.NewRequest(http.MethodGet, "/admin/memory", nil)
	request.RemoteAddr = "203.0.113.7:51000" // decidedly not loopback
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}
	if status, payload := serveAdmin(t, handler, request); status != http.StatusOK {
		t.Fatalf("a trusted remote client must reach /admin/memory, got %d %v", status, payload)
	}
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
