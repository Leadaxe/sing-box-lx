package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file pins the dial-context contract of the streamed modes (lx: SPEC
// 077): DialContext returns a stream-one/stream-up conn only once the HTTP
// layer has adopted the upload body, and after the return the dial context has
// no effect on the conn — the net.Dialer contract. The field case (2026-09-02,
// core lx.29): DNS servers `udp`/`tcp` with a detour through an XHTTP node in
// stream-one mode failed every query with "write request: context canceled" —
// the upstream DNS transport pool cancels its dial context the moment dial
// returns, and the former SPEC 050 guard, armed on that context until the
// download response (which stream-one only sends after the first write), tore
// the conn down before the first query. Never worked: before SPEC 072 the
// request itself rode the dial context, and `auto` + REALITY resolves to
// stream-one, so the default configuration was hit.

// TestStreamDialCancelAfterReturnKeepsConnAlive is the DNS-pool shape, against
// a live h2c server: cancel the dial context immediately after DialContext
// returns, then use the conn. Red on the pre-077 base ("context canceled" on
// the first Write, the guard still armed because the download response had not
// arrived), green after.
func TestStreamDialCancelAfterReturnKeepsConnAlive(t *testing.T) {
	for _, mode := range []string{modeStreamOne, modeStreamUp} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			addr := echoServer(t)
			client := liveH2CClient(t, addr, mode)

			// context.WithCancelCause + cancel(nil), exactly as
			// dns/transport/conn_pool.go does after its dial.
			ctx, cancel := context.WithCancelCause(context.Background())
			conn, err := client.DialContext(ctx)
			cancel(nil)
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			defer conn.Close()

			if _, err := conn.Write([]byte("hello")); err != nil {
				t.Fatalf("Write after the dial context was cancelled: %v", err)
			}
			// stream-one echoes the uplink on the response body; stream-up's
			// download GET carries a fixed banner.
			want := "hello"
			if mode == modeStreamUp {
				want = "downlink"
			}
			deadlineConn := conn.(interface{ SetReadDeadline(time.Time) error })
			deadlineConn.SetReadDeadline(time.Now().Add(writeFreeBudget))
			got := make([]byte, len(want))
			if _, err := io.ReadFull(conn, got); err != nil {
				t.Fatalf("Read after the dial context was cancelled: %v", err)
			}
			if string(got) != want {
				t.Fatalf("Read %q, want %q", got, want)
			}
		})
	}
}

// adoptingRoundTripper models a pooled connection that takes its time before
// reading the request body (a TCP+TLS dial, a stream slot), then reads it and
// answers 200. A bodiless request (stream-up's download GET) is answered only
// once the upload body has been adopted — the Xray ordering (SPEC 061), which
// is also what keeps this test honest: a download response arriving first
// legitimately ends the dial. The download body stays open like a real
// server's.
type adoptingRoundTripper struct {
	delay   time.Duration
	adopted atomic.Bool
	seen    chan struct{}
	once    sync.Once
}

func newAdoptingRoundTripper(delay time.Duration) *adoptingRoundTripper {
	return &adoptingRoundTripper{delay: delay, seen: make(chan struct{})}
}

func (rt *adoptingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		time.Sleep(rt.delay)
		rt.adopted.Store(true)
		rt.once.Do(func() { close(rt.seen) })
		go io.Copy(io.Discard, request.Body)
	} else {
		select {
		case <-rt.seen:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Body:       io.NopCloser(newBlockingReader()),
		Header:     make(http.Header),
	}, nil
}

// TestStreamDialWaitsForAdoption: the dial must not return before the HTTP
// layer has adopted the upload body. Red on the pre-077 base (DialContext
// returned instantly, before RoundTrip was even entered).
func TestStreamDialWaitsForAdoption(t *testing.T) {
	for _, mode := range []string{modeStreamOne, modeStreamUp} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			transport := newAdoptingRoundTripper(100 * time.Millisecond)
			client := stubClient(t, mode, transport)
			started := time.Now()
			conn, err := client.DialContext(context.Background())
			if err != nil {
				t.Fatalf("DialContext: %v", err)
			}
			defer conn.Close()
			if !transport.adopted.Load() {
				t.Fatal("DialContext returned before the upload body was adopted")
			}
			if elapsed := time.Since(started); elapsed < transport.delay {
				t.Fatalf("DialContext returned after %v, before the %v raise", elapsed, transport.delay)
			}
		})
	}
}

// TestStreamDialHalfAliveNodeFailsWithinDeadline is the SPEC 072 hole C shape
// under the new contract: the pooled connection never raises the stream (never
// reads the body, never answers). The dial must end with the dial context's
// error inside the caller's deadline — the WG bind dials under C.TCPTimeout —
// with the pending RoundTrip aborted and the pooled slot returned, so nothing
// is left to park a later Write on.
func TestStreamDialHalfAliveNodeFailsWithinDeadline(t *testing.T) {
	for _, mode := range []string{modeStreamOne, modeStreamUp} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			hang := &hangRoundTripper{}
			client := stubClient(t, mode, hang)

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := expectDialFailure(t, dialUnderTest(ctx, client), "")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("DialContext error = %v, want context.DeadlineExceeded", err)
			}
			if elapsed := time.Since(started); elapsed > writeFreeBudget {
				t.Fatalf("DialContext took %v, the dial context deadline did not bound the raise", elapsed)
			}
			// stream-up has two pending RoundTrips (download GET, upload POST);
			// both must die with the conn context.
			var pending int32 = 1
			if mode == modeStreamUp {
				pending = 2
			}
			waitObserved(t, hang, pending, "pending RoundTrip not aborted by the failed dial")
			if got := openUsageOf(client); got != 0 {
				t.Fatalf("openUsage = %d after a failed dial, want 0", got)
			}
		})
	}
}

// TestStreamDialSlotReleasedOnceOnRaiseFailure pins the double-release trap
// of the new contract: fail() releases the pooled slot when the raise fails,
// and the dial's own error path must not release it a second time — a
// negative openUsage would keep a retired connection alive forever.
func TestStreamDialSlotReleasedOnceOnRaiseFailure(t *testing.T) {
	t.Parallel()
	client := stubClient(t, modeStreamOne, errorRoundTripper{err: errors.New("pooled connection is dead")})
	for i := 0; i < 3; i++ {
		expectDialFailure(t, dialUnderTest(context.Background(), client), "")
	}
	if got := openUsageOf(client); got != 0 {
		t.Fatalf("openUsage = %d after three failed dials, want exactly 0", got)
	}
}
