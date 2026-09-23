package v2rayxhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/badh2"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
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

// TestRoundTripLocalCancelIsNeutral: context.Canceled is only ever produced by
// us (the conn-scoped cancel in Close) or the caller, so a burst of cancelled
// RoundTrips must neither retire the pooled connection nor arm the backoff.
// Deadlines and remote errors stay failures. lx: SPEC 094.
func TestRoundTripLocalCancelIsNeutral(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		calls int32
		cause string
	}{
		{"canceled", context.Canceled, xmuxBreakerThreshold + 1, ""},
		{"wrapped canceled", fmt.Errorf("x: %w", context.Canceled), xmuxBreakerThreshold + 1, ""},
		{"deadline", context.DeadlineExceeded, xmuxBreakerThreshold, "failing"},
		{"connection reset", syscall.ECONNRESET, xmuxBreakerThreshold, "failing"},
		{"stream error", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}, xmuxBreakerThreshold, "failing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			errs := make([]error, test.calls)
			for i := range errs {
				errs[i] = test.err
			}
			manager := singleTransportXmux(&failingRT{errs: errs})
			client, _ := manager.get()
			for i := int32(0); i < test.calls; i++ {
				if _, err := client.roundTrip(testRequest(t)); !errors.Is(err, test.err) {
					t.Fatalf("roundTrip err = %v, want %v", err, test.err)
				}
			}
			if cause := client.evictCause(); cause != test.cause {
				t.Fatalf("evictCause = %q, want %q", cause, test.cause)
			}
			if armed := manager.backoffArmed.Load(); armed != (test.cause != "") {
				t.Fatalf("backoff armed = %v, want %v", armed, test.cause != "")
			}
		})
	}
}

// bodyClosedReader is a download body after our own Body.Close(): every Read
// returns x/net's unexported "http2: response body closed" sentinel.
type bodyClosedReader struct {
	err error
}

func (r *bodyClosedReader) Read([]byte) (int, error) { return 0, r.err }
func (r *bodyClosedReader) Close() error             { return nil }

// TestReadAfterLocalCloseIsErrClosed: a read error on a body WE closed leaves
// the conn as net.ErrClosed, so the relay recognises a normal teardown; the
// same text without our Close, io.EOF and a remote StreamError pass through as
// before (SPEC 082 unchanged). lx: SPEC 094.
func TestReadAfterLocalCloseIsErrClosed(t *testing.T) {
	serverAddr := M.ParseSocksaddr("example.com:443")
	kinds := []struct {
		name string
		new  func(reader io.ReadCloser) net.Conn
	}{
		{"stream", func(reader io.ReadCloser) net.Conn {
			uploadReader, uploadWriter := io.Pipe()
			conn := newStreamConn(uploadReader, uploadWriter, serverAddr, nil)
			conn.setupReader(reader, nil)
			return conn
		}},
		{"split", func(reader io.ReadCloser) net.Conn {
			uploadReader, uploadWriter := io.Pipe()
			conn := newSplitConn(uploadReader, uploadWriter, serverAddr, nil)
			conn.setupReader(reader, nil)
			return conn
		}},
		{"packet", func(reader io.ReadCloser) net.Conn {
			conn := newPacketConn(context.Background(), &Client{}, "session", serverAddr, nil)
			conn.setupReader(reader, nil)
			return conn
		}},
	}
	bodyClosed := errors.New("http2: response body closed")
	for _, kind := range kinds {
		t.Run(kind.name, func(t *testing.T) {
			buffer := make([]byte, 16)

			conn := kind.new(&bodyClosedReader{err: bodyClosed})
			conn.Close()
			if _, err := conn.Read(buffer); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("after Close: Read err = %v, want net.ErrClosed", err)
			}

			conn = kind.new(&bodyClosedReader{err: bodyClosed})
			if _, err := conn.Read(buffer); err != bodyClosed {
				t.Fatalf("without Close: Read err = %v, want %v untouched", err, bodyClosed)
			}
			conn.Close()

			conn = kind.new(&bodyClosedReader{err: io.EOF})
			if _, err := conn.Read(buffer); err != io.EOF {
				t.Fatalf("without Close: Read err = %v, want io.EOF", err)
			}
			conn.Close()

			conn = kind.new(&bodyClosedReader{err: http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal}})
			var remote *badh2.RemoteStreamError
			if _, err := conn.Read(buffer); !errors.As(err, &remote) {
				t.Fatalf("without Close: Read err = %T %v, want *badh2.RemoteStreamError", err, err)
			}
			conn.Close()

			// A clean server finish racing our Close is still a clean finish.
			conn = kind.new(&bodyClosedReader{err: io.EOF})
			conn.Close()
			if _, err := conn.Read(buffer); err != io.EOF {
				t.Fatalf("after Close: Read err = %v, want io.EOF untouched", err)
			}

			// The read deadline closes the body too (SPEC 050); the caller must
			// still see a timeout, not a generic close.
			conn = kind.new(&bodyClosedReader{err: bodyClosed})
			if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Read(buffer); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("after expired read deadline: Read err = %v, want os.ErrDeadlineExceeded", err)
			}
			conn.Close()
		})
	}
}
