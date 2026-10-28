package v2rayxhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// SPECS/TASKS/141-XHTTP_PACKET_UP_PIPELINE
//
// packet-up uploads are batched and pipelined: the next POST goes out once the
// previous request body has been written, not once its response has been read;
// small writes coalesce into one POST; the outstanding responses are bounded;
// a failed POST fails the stream through Write and Read.

// postSeen is one upload POST as the stub server saw it.
type postSeen struct {
	seq  uint64
	body []byte
}

// pipelineTransport is a stub XHTTP server: the download GET holds its body
// open, every upload POST is drained, recorded in arrival order and fires the
// request's WroteRequest trace hook (what a real transport does once the body
// is on the wire); its response is held while gate(seq) is open and answers
// status(seq).
type pipelineTransport struct {
	access   sync.Mutex
	posts    []postSeen
	arrivals chan uint64
	gates    map[uint64]chan struct{}
	status   func(seq uint64) int
}

func newPipelineTransport() *pipelineTransport {
	return &pipelineTransport{
		arrivals: make(chan uint64, 64),
		gates:    make(map[uint64]chan struct{}),
	}
}

// hold makes the response of seq wait until release(seq).
func (t *pipelineTransport) hold(seq uint64) {
	t.access.Lock()
	t.gates[seq] = make(chan struct{})
	t.access.Unlock()
}

func (t *pipelineTransport) release(seq uint64) {
	t.access.Lock()
	gate := t.gates[seq]
	delete(t.gates, seq)
	t.access.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (t *pipelineTransport) seen() []postSeen {
	t.access.Lock()
	defer t.access.Unlock()
	return append([]postSeen(nil), t.posts...)
}

func (t *pipelineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		return &http.Response{StatusCode: http.StatusOK, Body: newBlockingBody()}, nil
	}
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		request.Body.Close()
	}
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	seq, err := strconv.ParseUint(segments[len(segments)-1], 10, 64)
	if err != nil {
		return nil, err
	}
	t.access.Lock()
	t.posts = append(t.posts, postSeen{seq: seq, body: body})
	gate := t.gates[seq]
	status := http.StatusOK
	if t.status != nil {
		status = t.status(seq)
	}
	t.access.Unlock()
	if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	t.arrivals <- seq
	if gate != nil {
		select {
		case <-gate:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	return &http.Response{StatusCode: status, Status: strconv.Itoa(status), Body: io.NopCloser(strings.NewReader(""))}, nil
}

// blockingBody is a download body that stays open until closed, as a real
// response body does: Close releases a blocked Read with an error.
type blockingBody struct {
	release chan struct{}
	once    sync.Once
}

func newBlockingBody() *blockingBody { return &blockingBody{release: make(chan struct{})} }

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.release
	return 0, errors.New("response body closed")
}

func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

// waitArrival waits for the next POST to reach the stub and returns its seq.
func (t *pipelineTransport) waitArrival(tb testing.TB, what string) uint64 {
	tb.Helper()
	select {
	case seq := <-t.arrivals:
		return seq
	case <-time.After(writeFreeBudget):
		tb.Fatalf("%s: no upload POST arrived", what)
		return 0
	}
}

// noArrival asserts that no POST reaches the stub within d.
func (t *pipelineTransport) noArrival(tb testing.TB, d time.Duration, what string) {
	tb.Helper()
	select {
	case seq := <-t.arrivals:
		tb.Fatalf("%s: POST %d arrived", what, seq)
	case <-time.After(d):
	}
}

func packetUpStubClient(t *testing.T, transport http.RoundTripper, opts metaOptions) *Client {
	t.Helper()
	client := stubClient(t, modePacketUp, transport)
	meta, err := normalizeMeta(opts, modePacketUp)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	client.meta = meta
	return client
}

// TestPacketUpNextPostBeforePreviousResponse is the verification issue #58
// asks for: POST 0's response is held after its body was consumed, and POST 1
// must arrive before that response is released.
func TestPacketUpNextPostBeforePreviousResponse(t *testing.T) {
	t.Parallel()
	transport := newPipelineTransport()
	transport.hold(0)
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "0"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("first")); err != nil {
		t.Fatalf("Write 0: %v", err)
	}
	if seq := transport.waitArrival(t, "post 0"); seq != 0 {
		t.Fatalf("first POST has seq %d, want 0", seq)
	}
	if _, err := conn.Write([]byte("second")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if seq := transport.waitArrival(t, "post 1 while post 0's response is held"); seq != 1 {
		t.Fatalf("second POST has seq %d, want 1", seq)
	}
	transport.release(0)
}

// TestPacketUpBatchesSmallWrites: writes made while the loop is paced or busy
// coalesce into one POST, in order and complete.
func TestPacketUpBatchesSmallWrites(t *testing.T) {
	t.Parallel()
	transport := newPipelineTransport()
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "200"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	var want bytes.Buffer
	const writes = 20
	for i := 0; i < writes; i++ {
		chunk := []byte("write-" + strconv.Itoa(i) + ";")
		want.Write(chunk)
		if _, err := conn.Write(chunk); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	var got bytes.Buffer
	deadline := time.Now().Add(writeFreeBudget)
	for got.Len() < want.Len() {
		if time.Now().After(deadline) {
			t.Fatalf("server received %d of %d bytes", got.Len(), want.Len())
		}
		time.Sleep(10 * time.Millisecond)
		got.Reset()
		for _, post := range transport.seen() {
			got.Write(post.body)
		}
	}
	posts := transport.seen()
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("server reassembled %q, want %q", got.String(), want.String())
	}
	if len(posts) >= writes {
		t.Fatalf("%d writes became %d POSTs — no batching", writes, len(posts))
	}
	for i, post := range posts {
		if post.seq != uint64(i) {
			t.Fatalf("POST %d carries seq %d", i, post.seq)
		}
	}
}

// TestPacketUpLargeWriteSplit: a write above sc_max_each_post_bytes is
// delivered in sequenced pieces no larger than the bound, nothing lost.
func TestPacketUpLargeWriteSplit(t *testing.T) {
	t.Parallel()
	transport := newPipelineTransport()
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "0", ScMaxEachPostBytes: "1000"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	payload := bytes.Repeat([]byte("0123456789"), 350) // 3500 bytes
	n, err := conn.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	var got bytes.Buffer
	deadline := time.Now().Add(writeFreeBudget)
	for got.Len() < len(payload) {
		if time.Now().After(deadline) {
			t.Fatalf("server received %d of %d bytes", got.Len(), len(payload))
		}
		time.Sleep(10 * time.Millisecond)
		got.Reset()
		for _, post := range transport.seen() {
			if len(post.body) > 1000 {
				t.Fatalf("POST %d carries %d bytes, above sc_max_each_post_bytes", post.seq, len(post.body))
			}
			got.Write(post.body)
		}
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatal("server reassembled a different payload")
	}
}

// TestPacketUpInflightBound: with the outstanding-response cap at two, a third
// POST is not issued until one of the held responses is released.
func TestPacketUpInflightBound(t *testing.T) {
	// NOT parallel: overrides packetUpMaxInflight.
	restore := packetUpMaxInflight
	packetUpMaxInflight = 2
	t.Cleanup(func() { packetUpMaxInflight = restore })

	transport := newPipelineTransport()
	transport.hold(0)
	transport.hold(1)
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "0"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	for i := 0; i < 2; i++ {
		if _, err := conn.Write([]byte("held")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		transport.waitArrival(t, "held post")
	}
	if _, err := conn.Write([]byte("third")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	transport.noArrival(t, 300*time.Millisecond, "two responses outstanding at a cap of two")
	transport.release(0)
	if seq := transport.waitArrival(t, "post after a response was released"); seq != 2 {
		t.Fatalf("POST after release has seq %d, want 2", seq)
	}
	transport.release(1)
}

// TestPacketUpFailedPostFailsStream: a non-200 upload response is a lost
// packet — the server's reorder queue never fills the hole — so the stream
// must fail: Write and Read both return the cause.
func TestPacketUpFailedPostFailsStream(t *testing.T) {
	t.Parallel()
	transport := newPipelineTransport()
	transport.status = func(seq uint64) int {
		if seq == 1 {
			return http.StatusBadGateway
		}
		return http.StatusOK
	}
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "0"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatalf("Write 0: %v", err)
	}
	transport.waitArrival(t, "post 0")
	if _, err := conn.Write([]byte("refused")); err != nil {
		t.Fatalf("Write 1 (buffered): %v", err)
	}
	transport.waitArrival(t, "post 1")

	deadline := time.Now().Add(writeFreeBudget)
	var writeErr error
	for writeErr == nil {
		if time.Now().After(deadline) {
			t.Fatal("Write keeps succeeding after a refused POST")
		}
		time.Sleep(10 * time.Millisecond)
		_, writeErr = conn.Write([]byte("after"))
	}
	if !strings.Contains(writeErr.Error(), "unexpected upload status") {
		t.Fatalf("Write error = %v, want the upload status", writeErr)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), "unexpected upload status") {
		t.Fatalf("Read after a refused POST = %v, want the upload status", err)
	}
}

// TestPacketUpCloseWithHeldResponses: Close must not wait for the server —
// held responses are cancelled, the pooled connection is released once.
func TestPacketUpCloseWithHeldResponses(t *testing.T) {
	t.Parallel()
	transport := newPipelineTransport()
	transport.hold(0)
	client := packetUpStubClient(t, transport, metaOptions{ScMinPostsIntervalMs: "0"})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	if _, err := conn.Write([]byte("held")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	transport.waitArrival(t, "held post")

	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()
	select {
	case <-closed:
	case <-time.After(writeFreeBudget):
		t.Fatal("Close blocked on a held upload response")
	}
	if got := openUsageOf(client); got != 0 {
		t.Fatalf("openUsage = %d after Close, want 0", got)
	}
	if _, err := conn.Write([]byte("after close")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write after Close = %v, want net.ErrClosed", err)
	}
}

// TestPacketUpLiveH2Pipelined is issue #58's fixture over a real HTTP/2
// transport: sixteen 32 KiB writes against a server that delays every POST
// response by 50 ms. Serial posts would need at least 16 × 50 ms; pipelined
// ones finish well under that, with a gapless seq set and every byte.
func TestPacketUpLiveH2Pipelined(t *testing.T) {
	t.Parallel()
	const chunk = 32 * 1024
	const chunks = 16
	const delay = 50 * time.Millisecond

	var received atomic.Int64
	var order []uint64
	var orderAccess sync.Mutex
	addr := delayedPostServer(t, delay, func(seq uint64, n int64) {
		orderAccess.Lock()
		order = append(order, seq)
		orderAccess.Unlock()
		received.Add(n)
	})
	client := liveH2CClient(t, addr, modePacketUp)
	meta, err := normalizeMeta(metaOptions{ScMinPostsIntervalMs: "0", ScMaxEachPostBytes: "1000000"}, modePacketUp)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	client.meta = meta
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	started := time.Now()
	payload := bytes.Repeat([]byte{0xAB}, chunk)
	for i := 0; i < chunks; i++ {
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(writeFreeBudget)
	for received.Load() < chunk*chunks {
		if time.Now().After(deadline) {
			t.Fatalf("server received %d of %d bytes", received.Load(), chunk*chunks)
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(started)
	if elapsed >= chunks*delay/2 {
		t.Fatalf("upload took %v — serial posts (≥ %v) rather than pipelined", elapsed, chunks*delay)
	}
	// Handlers of concurrent h2 streams finish in scheduling order, not wire
	// order (that is what the server's reorder queue is for): assert the seqs
	// form a gapless 0..n-1 set, not their handler order.
	orderAccess.Lock()
	defer orderAccess.Unlock()
	seen := make(map[uint64]bool, len(order))
	for _, seq := range order {
		if seen[seq] {
			t.Fatalf("seq %d posted twice: %v", seq, order)
		}
		seen[seq] = true
	}
	for seq := uint64(0); seq < uint64(len(order)); seq++ {
		if !seen[seq] {
			t.Fatalf("seq %d missing from %v", seq, order)
		}
	}
}

// h2cServer serves handler over h2c on a loopback listener for the test's
// lifetime and returns its address.
func h2cServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}

// delayedPostServer is an h2c XHTTP stand-in whose upload POSTs are drained,
// reported through onPost, and answered 200 only after delay; the download GET
// stays open.
func delayedPostServer(t *testing.T, delay time.Duration, onPost func(seq uint64, n int64)) string {
	t.Helper()
	return h2cServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			return
		}
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		seq, _ := strconv.ParseUint(segments[len(segments)-1], 10, 64)
		onPost(seq, n)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
