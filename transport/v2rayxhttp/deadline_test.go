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
// the request body — used to block a writer forever: the streamed conn hands
// itself up before RoundTrip has raised the stream, the body is an io.Pipe, and
// nothing on the conn could interrupt a pending Write. These tests pin the two
// escapes: a write deadline, and cancellation of the dial context.

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

// hangingClient builds a stream-one client whose transport never reads the body.
func hangingClient(t *testing.T) (*Client, *hangingTransport) {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{}, modeStreamOne)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	transport := newHangingTransport()
	client := &Client{
		scheme:     "https",
		host:       "example.com",
		serverAddr: M.ParseSocksaddr("example.com:443"),
		path:       "/xhttp",
		mode:       modeStreamOne,
		meta:       meta,
		transport:  transport,
	}
	return client, transport
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

// TestStreamOneWriteDeadlineUnblocksWrite is the core of the bug: without a
// working SetWriteDeadline a Write into the upload pipe of a half-alive node
// never returns, and the goroutine holding it outlives the whole box.
func TestStreamOneWriteDeadlineUnblocksWrite(t *testing.T) {
	client, transport := hangingClient(t)
	defer transport.Close()

	conn, err := client.dialStreamOne(context.Background(), "")
	if err != nil {
		t.Fatalf("dialStreamOne: %v", err)
	}
	defer conn.Close()

	<-transport.entered

	if err := conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}

	select {
	case result := <-writeAsync(conn):
		if result.err == nil {
			t.Fatal("Write returned without error: the body has no reader, it must not succeed")
		}
		if !errors.Is(result.err, os.ErrDeadlineExceeded) {
			t.Fatalf("Write error = %v, want os.ErrDeadlineExceeded", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write still blocked after the write deadline expired (zombie goroutine)")
	}
}

// TestStreamOneDialCancelUnblocksWrite covers the path that the URL test itself
// takes: the encryption handshake runs on a bare net.Conn with no context, so
// cancelling the dial context is what has to free a pending Write.
func TestStreamOneDialCancelUnblocksWrite(t *testing.T) {
	client, transport := hangingClient(t)
	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := client.dialStreamOne(ctx, "")
	if err != nil {
		t.Fatalf("dialStreamOne: %v", err)
	}
	defer conn.Close()

	<-transport.entered

	done := writeAsync(conn)
	select {
	case result := <-done:
		t.Fatalf("Write returned before cancellation: n=%d err=%v", result.n, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case result := <-done:
		if result.err == nil {
			t.Fatal("Write succeeded after the dial context was cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the dial context did not release the pending Write")
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

// TestStreamOneCancelAfterStreamUpKeepsConnAlive is the R4 guard for the dial
// watcher: cancelling the dial context is routine once the stream is up (the
// caller's dial is over), and it must not tear the connection down. A watcher
// that outlived `created` would break every live stream-one connection.
func TestStreamOneCancelAfterStreamUpKeepsConnAlive(t *testing.T) {
	bodyReader, bodyWriter := io.Pipe()
	defer bodyWriter.Close()

	meta, err := normalizeMeta(metaOptions{}, modeStreamOne)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	client := &Client{
		scheme:     "https",
		host:       "example.com",
		serverAddr: M.ParseSocksaddr("example.com:443"),
		path:       "/xhttp",
		mode:       modeStreamOne,
		meta:       meta,
		transport:  &liveTransport{body: bodyReader},
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := client.dialStreamOne(ctx, "")
	if err != nil {
		t.Fatalf("dialStreamOne: %v", err)
	}
	defer conn.Close()

	// Wait for the stream to be up, then cancel: this is the normal lifecycle of
	// a dial context, not an abort.
	streamConn := conn.(*streamConn)
	<-streamConn.created
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
	conn := newStreamConn(pipeReader, pipeWriter, M.ParseSocksaddr("example.com:443"))
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
