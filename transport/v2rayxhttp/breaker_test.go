package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// SPECS/TASKS/076-XHTTP_XMUX_BREAKER
//
// The breaker exists for one field failure mode (issue #14): a path that kills
// every stream turns the client into an unthrottled dial→reset→dial loop that
// pins the CPU until the core is restarted. These tests pin the damping: a
// connection that fails streams consecutively is retired, opening replacements
// is throttled with a doubling backoff, and genuine success resets everything.
// Equally important is what must NOT trip it: clean EOFs and our own local
// teardown.

// failingRT is a RoundTripper scripted per call: an error entry returns that
// error, a status entry returns an empty-bodied response with that status.
type failingRT struct {
	errs     []error
	statuses []int
	calls    int
}

func (rt *failingRT) RoundTrip(*http.Request) (*http.Response, error) {
	i := rt.calls
	rt.calls++
	if i < len(rt.errs) && rt.errs[i] != nil {
		return nil, rt.errs[i]
	}
	status := http.StatusOK
	if i < len(rt.statuses) && rt.statuses[i] != 0 {
		status = rt.statuses[i]
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func testRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// TestBreakerTripsAndEvicts: xmuxBreakerThreshold consecutive failures retire
// the connection — evictCause reports it, the next get() opens a replacement
// and tears the failed one down.
func TestBreakerTripsAndEvicts(t *testing.T) {
	manager, conns := poolOf(t, xmuxConfig{maxConcurrency: intRange{4, 4}})
	client, _ := manager.get()
	for i := int32(0); i < xmuxBreakerThreshold; i++ {
		if cause := client.evictCause(); cause != "" {
			t.Fatalf("evictCause before threshold = %q", cause)
		}
		client.noteFailure()
	}
	if cause := client.evictCause(); cause != "failing" {
		t.Fatalf("evictCause after threshold = %q, want failing", cause)
	}
	manager.resetBackoff() // isolate eviction from the backoff gate
	replacement, _ := manager.get()
	if replacement == client {
		t.Fatal("get() returned the failing connection")
	}
	if got := conns(); len(got) != 2 || got[0].closes() == 0 {
		t.Fatalf("want 2 conns with the first closed, got %d conns, first closes=%d", len(got), got[0].closes())
	}
}

// TestBreakerSuccessResetsStreak: failures must be CONSECUTIVE — a success in
// between starts the count over.
func TestBreakerSuccessResetsStreak(t *testing.T) {
	manager, _ := poolOf(t, xmuxConfig{})
	client, _ := manager.get()
	client.noteFailure()
	client.noteFailure()
	client.noteSuccess()
	client.noteFailure()
	client.noteFailure()
	if client.failing.Load() {
		t.Fatal("tripped without threshold consecutive failures")
	}
	client.noteFailure()
	if !client.failing.Load() {
		t.Fatal("did not trip on threshold consecutive failures")
	}
}

// TestBackoffDoublesAndCaps: each trip doubles the window within
// [xmuxBackoffInitial, xmuxBackoffCap]; success disarms it.
func TestBackoffDoublesAndCaps(t *testing.T) {
	manager, _ := poolOf(t, xmuxConfig{})
	want := xmuxBackoffInitial
	for i := 0; i < 10; i++ {
		manager.noteBreakerTrip()
		manager.access.Lock()
		got := manager.backoffDelay
		manager.access.Unlock()
		if got != want {
			t.Fatalf("trip %d: backoff = %s, want %s", i+1, got, want)
		}
		if want *= 2; want > xmuxBackoffCap {
			want = xmuxBackoffCap
		}
	}
	manager.resetBackoff()
	manager.access.Lock()
	defer manager.access.Unlock()
	if manager.backoffDelay != 0 || manager.backoffArmed.Load() {
		t.Fatal("resetBackoff did not disarm")
	}
}

// TestBackoffGatesOnlyEmptyPool: inside the window an empty pool yields
// (nil, wait); a pool with a live connection hands it out without waiting.
func TestBackoffGatesOnlyEmptyPool(t *testing.T) {
	base := time.Now()
	savedNow := timeNow
	timeNow = func() time.Time { return base }
	defer func() { timeNow = savedNow }()

	manager, _ := poolOf(t, xmuxConfig{maxConcurrency: intRange{4, 4}})
	manager.noteBreakerTrip()
	client, wait := manager.get()
	if client != nil || wait <= 0 {
		t.Fatalf("empty pool in window: got (%v, %s), want (nil, >0)", client, wait)
	}
	// The window elapses: opening is allowed again.
	timeNow = func() time.Time { return base.Add(xmuxBackoffInitial + time.Millisecond) }
	client, _ = manager.get()
	if client == nil {
		t.Fatal("get() still blocked after the window elapsed")
	}
	// A live pooled connection is handed out even inside a fresh window.
	manager.noteBreakerTrip()
	pooled, wait := manager.get()
	if pooled != client || wait != 0 {
		t.Fatalf("live connection not handed out inside window: (%v, %s)", pooled, wait)
	}
}

// TestGetContextWaitsOutWindow: getContext sleeps through the window and then
// opens; a cancelled context aborts the wait instead.
func TestGetContextWaitsOutWindow(t *testing.T) {
	savedInitial := xmuxBackoffInitial
	xmuxBackoffInitial = 10 * time.Millisecond
	defer func() { xmuxBackoffInitial = savedInitial }()

	manager, _ := poolOf(t, xmuxConfig{})
	manager.noteBreakerTrip()
	client, err := manager.getContext(context.Background())
	if err != nil || client == nil {
		t.Fatalf("getContext = (%v, %v), want client", client, err)
	}

	manager2, _ := poolOf(t, xmuxConfig{})
	manager2.noteBreakerTrip()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager2.getContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled getContext err = %v, want context.Canceled", err)
	}
}

// TestRoundTripFeedsBreaker: transport errors and non-200 responses count as
// failures; a 200 is neutral (headers are not proof of a living stream).
func TestRoundTripFeedsBreaker(t *testing.T) {
	rt := &failingRT{
		errs:     []error{errors.New("reset"), nil, nil},
		statuses: []int{0, http.StatusBadGateway, http.StatusOK},
	}
	manager := singleTransportXmux(rt)
	client, _ := manager.get()

	client.roundTrip(testRequest(t)) // transport error
	if got := client.consecFails.Load(); got != 1 {
		t.Fatalf("after transport error: consecFails = %d, want 1", got)
	}
	response, _ := client.roundTrip(testRequest(t)) // 502
	response.Body.Close()
	if got := client.consecFails.Load(); got != 2 {
		t.Fatalf("after 502: consecFails = %d, want 2", got)
	}
	response, _ = client.roundTrip(testRequest(t)) // 200 — neutral, no reset either
	response.Body.Close()
	if got := client.consecFails.Load(); got != 2 {
		t.Fatalf("after 200: consecFails = %d, want 2 (headers are not success)", got)
	}
}

// TestNoteReadClassification pins the read-side rules: first successful read =
// success, remote error = failure, EOF and locally-initiated teardown = neutral.
func TestNoteReadClassification(t *testing.T) {
	manager, _ := poolOf(t, xmuxConfig{})
	client, _ := manager.get()
	client.noteFailure()
	breaker := connBreaker{xmux: newXmuxRelease(client)}

	breaker.noteRead(nil) // first successful read resets the streak
	if got := client.consecFails.Load(); got != 0 {
		t.Fatalf("after successful read: consecFails = %d, want 0", got)
	}
	breaker.noteRead(io.EOF) // clean server finish — neutral
	if got := client.consecFails.Load(); got != 0 {
		t.Fatalf("after EOF: consecFails = %d, want 0", got)
	}
	breaker.noteRead(errors.New("stream error: INTERNAL_ERROR")) // remote death
	if got := client.consecFails.Load(); got != 1 {
		t.Fatalf("after remote error: consecFails = %d, want 1", got)
	}
	breaker.localClosed.Store(true)
	breaker.noteRead(errors.New("http2: response body closed")) // our own close
	if got := client.consecFails.Load(); got != 1 {
		t.Fatalf("after local close: consecFails = %d, want 1 (unchanged)", got)
	}
}

// TestUplinkBodyGetBody: packet-up upload requests must be transparently
// retryable — GetBody replays the same payload from offset zero, so http2 can
// re-issue the POST after a graceful GOAWAY instead of killing the session.
func TestUplinkBodyGetBody(t *testing.T) {
	client := &Client{}
	request := testRequest(t)
	payload := []byte("uplink payload")
	client.applyUplinkData(request, payload)
	if request.GetBody == nil {
		t.Fatal("GetBody not set on body-placement upload")
	}
	for i := 0; i < 2; i++ {
		body, err := request.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		replay, err := io.ReadAll(body)
		if err != nil || string(replay) != string(payload) {
			t.Fatalf("GetBody replay %d = %q, %v", i, replay, err)
		}
	}
}
