package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART
//
// A half-alive XHTTP node — one that accepts the TCP connection but never reads
// the request body — used to block a writer forever: the streamed conn handed
// itself up before RoundTrip had raised the stream, the body is an io.Pipe, and
// nothing on the conn could interrupt a pending Write. These tests pin the two
// escapes. The write deadline (R1) is unchanged. The dial-context escape (R2)
// moved INTO the dial with SPEC 077: DialContext no longer returns before the
// HTTP layer has adopted the upload body, so a dial context cancelled before
// the raise fails the dial itself — there is no conn, hence no Write to free —
// and a dial context cancelled after the return has no effect on the conn.

// hangingTransport models the half-alive server: the TCP connection is accepted
// (RoundTrip is entered) but the response never arrives and the request body is
// never read, so the upload pipe has no reader.
type hangingTransport struct {
	entered chan struct{}
	release chan struct{}
}

func newHangingTransport() *hangingTransport {
	return &hangingTransport{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (t *hangingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	select {
	case t.entered <- struct{}{}:
	default:
	}
	<-t.release
	return nil, errors.New("released")
}

func (t *hangingTransport) Close() error {
	close(t.release)
	return nil
}

// stallingTransport models a node that raised the stream and then stopped
// draining it: the request body is adopted (one byte is read, which is what
// lets the dial return since SPEC 077), after which nothing reads the pipe and
// no response ever arrives. This is the post-077 shape of the half-alive node
// for the write deadline: a dead peer behind a live front, or an exhausted h2
// flow-control window.
type stallingTransport struct {
	release chan struct{}
}

func newStallingTransport() *stallingTransport {
	return &stallingTransport{release: make(chan struct{})}
}

func (t *stallingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	go request.Body.Read(make([]byte, 1))
	<-t.release
	return nil, errors.New("released")
}

func (t *stallingTransport) Close() error {
	close(t.release)
	return nil
}

// streamOneClient builds a stream-one client around a caller-supplied transport.
func streamOneClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{}, modeStreamOne)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	return &Client{
		scheme:     "https",
		host:       "example.com",
		serverAddr: M.ParseSocksaddr("example.com:443"),
		path:       "/xhttp",
		mode:       modeStreamOne,
		meta:       meta,
		xmux:       singleTransportXmux(transport),
	}
}

// hangingClient builds a stream-one client whose transport never reads the body.
func hangingClient(t *testing.T) (*Client, *hangingTransport) {
	t.Helper()
	transport := newHangingTransport()
	return streamOneClient(t, transport), transport
}

// writeResult carries the outcome of a Write issued from a helper goroutine.
type writeResult struct {
	n   int
	err error
}

// writeAsync issues a blocking Write on conn and reports the outcome.
func writeAsync(conn interface{ Write([]byte) (int, error) }) <-chan writeResult {
	done := make(chan writeResult, 1)
	go func() {
		n, err := conn.Write([]byte("handshake"))
		done <- writeResult{n, err}
	}()
	return done
}

// dialResult carries the outcome of a dial issued from a helper goroutine.
type dialResult struct {
	conn interface{ Close() error }
	err  error
}

// dialStreamOneAsync runs dialStreamOne in a goroutine — since SPEC 077 the
// dial parks until the raise, so a test that drives the raise itself must not
// call it inline.
func dialStreamOneAsync(ctx context.Context, client *Client) (<-chan dialResult, *xmuxClient) {
	xc, _ := client.xmux.get()
	xc.addOpenUsage(1)
	done := make(chan dialResult, 1)
	go func() {
		conn, err := client.dialStreamOne(ctx, "", xc, newXmuxRelease(xc))
		done <- dialResult{conn, err}
	}()
	return done, xc
}

// TestStreamOneWriteDeadlineUnblocksWrite is the core of the bug: without a
// working SetWriteDeadline a Write into the upload pipe of a node that stopped
// draining never returns, and the goroutine holding it outlives the whole box.
func TestStreamOneWriteDeadlineUnblocksWrite(t *testing.T) {
	transport := newStallingTransport()
	defer transport.Close()
	client := streamOneClient(t, transport)

	xc, _ := client.xmux.get()
	conn, err := client.dialStreamOne(context.Background(), "", xc, newXmuxRelease(xc))
	if err != nil {
		t.Fatalf("dialStreamOne: %v", err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}

	select {
	case result := <-writeAsync(conn):
		if result.err == nil {
			t.Fatal("Write returned without error: the body is not drained, it must not succeed")
		}
		if !errors.Is(result.err, os.ErrDeadlineExceeded) {
			t.Fatalf("Write error = %v, want os.ErrDeadlineExceeded", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write still blocked after the write deadline expired (zombie goroutine)")
	}
}

// TestStreamOneDialCancelBeforeRaiseFailsDial covers the path the URL test
// takes on a half-alive node: the encryption handshake runs on a bare net.Conn
// with no context, so the dial context is the only handle the caller has. It
// must end the dial — with the context's error, the pooled slot returned — and
// never hand up a conn whose first Write would park on an unread pipe (the
// pre-077 shape, where the guard freed that Write instead).
func TestStreamOneDialCancelBeforeRaiseFailsDial(t *testing.T) {
	client, transport := hangingClient(t)
	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done, xc := dialStreamOneAsync(ctx, client)

	<-transport.entered

	select {
	case result := <-done:
		t.Fatalf("dial returned before the raise: conn=%v err=%v", result.conn, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case result := <-done:
		if result.err == nil {
			result.conn.Close()
			t.Fatal("dial succeeded after the dial context was cancelled before the raise")
		}
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("dial error = %v, want context.Canceled", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the dial context did not end the parked dial")
	}
	if got := xc.getOpenUsage(); got != 0 {
		t.Fatalf("openUsage = %d after a failed dial, want 0 (slot not returned)", got)
	}
}

// liveTransport answers immediately and keeps reading the request body, which is
// what a healthy XHTTP server does.
type liveTransport struct {
	body io.ReadCloser
}

func (t *liveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	go io.Copy(io.Discard, request.Body)
	return &http.Response{StatusCode: http.StatusOK, Body: t.body}, nil
}

// TestStreamOneCancelAfterStreamUpKeepsConnAlive is the R4 guard: cancelling
// the dial context is routine once the dial has returned (net.Dialer callers
// do it in a defer, the DNS transport pool does it immediately — SPEC 077),
// and it must not tear the connection down.
func TestStreamOneCancelAfterStreamUpKeepsConnAlive(t *testing.T) {
	bodyReader, bodyWriter := io.Pipe()
	defer bodyWriter.Close()

	client := streamOneClient(t, &liveTransport{body: bodyReader})

	ctx, cancel := context.WithCancel(context.Background())
	xc, _ := client.xmux.get()
	conn, err := client.dialStreamOne(ctx, "", xc, newXmuxRelease(xc))
	if err != nil {
		t.Fatalf("dialStreamOne: %v", err)
	}
	defer conn.Close()

	// The dial is over: this is the normal lifecycle of a dial context, not an
	// abort.
	cancel()

	select {
	case result := <-writeAsync(conn):
		if result.err != nil {
			t.Fatalf("Write on a live conn failed after the dial context was cancelled: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write on a live conn blocked after the dial context was cancelled")
	}
}

// TestStreamOneDeadlineDoesNotBreakLiveConn guards R4: once the stream is up the
// dial context is done, and that must not disturb a working connection.
func TestStreamOneDeadlineDoesNotBreakLiveConn(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	conn := newStreamConn(pipeReader, pipeWriter, M.ParseSocksaddr("example.com:443"), nil)
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("clearing the write deadline must be accepted: %v", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clearing the read deadline must be accepted: %v", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("clearing both deadlines must be accepted: %v", err)
	}
}
