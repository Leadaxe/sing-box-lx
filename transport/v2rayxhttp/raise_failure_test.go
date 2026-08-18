package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// This file pins the raise-failure contract (lx: SPEC 072): a dial whose HTTP
// side fails — RoundTrip error, non-200 status, dead pooled connection — must
// break the upload pipe so a blocked or future Write returns the failure
// instead of hanging forever. The field dump behind it (2026-08-17, core
// lx.27-rc.2) shows the pre-fix behaviour: the SPEC 050 dial-context guard
// stands down when `created` closes, and the error branches of setupReader
// closed `created` without breaking the pipe — a VLESS handshake Write then
// blocked for 38 minutes on a pipe nobody would ever read, welding the WG
// bind, the pause chain and the endpoint manager into a process-wide freeze.
//
// The second half of the contract is conn lifetime (also SPEC 072): requests
// ride a conn-scoped context under the transport's lifetime ctx, NOT the dial
// context — a bounded dial context (the WG bind dials with C.TCPTimeout since
// SPEC 071) must stop bounding the stream the moment it is raised, otherwise
// every healthy detour conn is torn down at the deadline and the endpoint
// cycles every 15 s.

// stubClient assembles a Client around a fixed RoundTripper, the same way
// h2cClient does but without a live server.
func stubClient(t *testing.T, mode string, transport http.RoundTripper) *Client {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{}, mode)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	return &Client{
		ctx:          context.Background(),
		serverAddr:   M.ParseSocksaddr("127.0.0.1:443"),
		xmux:         singleTransportXmux(transport),
		scheme:       "http",
		host:         "example.com",
		path:         "/xhttp/",
		mode:         mode,
		headers:      make(http.Header),
		paddingRange: intRange{0, 0},
		meta:         meta,
	}
}

// errorRoundTripper fails every request without touching the body — the shape
// of a dead pooled connection (x/net http2 does not close the request body
// when it errors before adopting the request).
type errorRoundTripper struct {
	err error
}

func (rt errorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, rt.err
}

// statusRoundTripper answers every request with a fixed status and an empty
// body, never reading the request body.
type statusRoundTripper struct {
	code int
}

func (rt statusRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: rt.code,
		Status:     http.StatusText(rt.code),
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

// methodRoundTripper routes requests by method, so stream-up's paired GET/POST
// can fail independently.
type methodRoundTripper struct {
	get  http.RoundTripper
	post http.RoundTripper
}

func (rt methodRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		return rt.get.RoundTrip(request)
	}
	return rt.post.RoundTrip(request)
}

// hangRoundTripper parks every request until its context dies, recording that
// it observed the cancellation. It never reads the request body — the pipe
// stays unread, exactly like a wedged pooled connection.
type hangRoundTripper struct {
	observed atomic.Int32
}

func (rt *hangRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	rt.observed.Add(1)
	return nil, request.Context().Err()
}

// writeUnderTest runs one conn.Write in a goroutine and reports its result.
func writeUnderTest(conn net.Conn, payload []byte) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		result <- err
	}()
	return result
}

const writeFreeBudget = 3 * time.Second

// TestStreamOneWriteFreedOnRoundTripError: a stream-one dial whose RoundTrip
// fails must free the handshake Write with that error. Red on the pre-fix
// base: the guard stands down on `created`, nobody reads the pipe, the Write
// hangs past any budget.
func TestStreamOneWriteFreedOnRoundTripError(t *testing.T) {
	t.Parallel()
	dialErr := errors.New("pooled connection is dead")
	client := stubClient(t, modeStreamOne, errorRoundTripper{err: dialErr})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	select {
	case err := <-writeUnderTest(conn, []byte("vless request header")):
		if err == nil {
			t.Fatal("Write succeeded on a conn whose RoundTrip failed")
		}
		if !strings.Contains(err.Error(), dialErr.Error()) {
			t.Fatalf("Write error %q does not carry the RoundTrip failure %q", err, dialErr)
		}
	case <-time.After(writeFreeBudget):
		t.Fatal("Write still blocked after the raise failed — upload pipe was not broken")
	}
}

// TestStreamOneWriteFreedOnBadStatus: same contract for a non-200 response.
func TestStreamOneWriteFreedOnBadStatus(t *testing.T) {
	t.Parallel()
	client := stubClient(t, modeStreamOne, statusRoundTripper{code: http.StatusBadGateway})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	select {
	case err := <-writeUnderTest(conn, []byte("vless request header")):
		if err == nil {
			t.Fatal("Write succeeded on a conn whose raise answered 502")
		}
	case <-time.After(writeFreeBudget):
		t.Fatal("Write still blocked after a non-200 raise — upload pipe was not broken")
	}
}

// TestStreamUpWriteFreedOnDownloadError: stream-up's download GET failing must
// break the upload pipe even while the upload POST is still pending — the conn
// can never carry protocol bytes once the download side is dead.
func TestStreamUpWriteFreedOnDownloadError(t *testing.T) {
	t.Parallel()
	downErr := errors.New("download stream refused")
	hang := &hangRoundTripper{}
	client := stubClient(t, modeStreamUp, methodRoundTripper{
		get:  errorRoundTripper{err: downErr},
		post: hang,
	})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	select {
	case err := <-writeUnderTest(conn, []byte("vless request header")):
		if err == nil {
			t.Fatal("Write succeeded on a conn whose download raise failed")
		}
	case <-time.After(writeFreeBudget):
		t.Fatal("Write still blocked after the download raise failed — upload pipe was not broken")
	}
}

// TestStreamUpWriteCarriesUploadError: when the upload POST itself fails, the
// blocked writer must see that error, not a bare io.ErrClosedPipe (the read
// half is the side that surfaces an error to a pipe writer).
func TestStreamUpWriteCarriesUploadError(t *testing.T) {
	t.Parallel()
	upErr := errors.New("upload stream refused")
	hang := &hangRoundTripper{}
	client := stubClient(t, modeStreamUp, methodRoundTripper{
		get:  hang, // download pending: isolates the upload-failure path
		post: errorRoundTripper{err: upErr},
	})
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	select {
	case err := <-writeUnderTest(conn, []byte("vless request header")):
		if err == nil {
			t.Fatal("Write succeeded on a conn whose upload raise failed")
		}
		if !strings.Contains(err.Error(), upErr.Error()) {
			t.Fatalf("Write error %q lost the upload failure %q", err, upErr)
		}
	case <-time.After(writeFreeBudget):
		t.Fatal("Write still blocked after the upload raise failed")
	}
}

// echoServer is an h2c server that raises every stream immediately: uploads
// are drained, downloads answer 200 and echo a banner. It stands in for a
// healthy XHTTP server so conn-lifetime tests can watch what a DIAL context
// deadline does to a LIVE conn.
func echoServer(t *testing.T) string {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if r.Method == http.MethodGet {
			w.Write([]byte("downlink"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		// Upload (or stream-one): echo the request body back chunk by chunk, so
		// stream-one gets a live downlink and packet-up posts drain.
		buffer := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buffer)
			if n > 0 {
				w.Write(buffer[:n])
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}

// liveH2CClient builds a Client with a real h2c transport at addr, mode given.
func liveH2CClient(t *testing.T, addr, mode string) *Client {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{}, mode)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	return &Client{
		ctx:        context.Background(),
		serverAddr: M.ParseSocksaddr(addr),
		xmux: singleTransportXmux(&http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		}),
		scheme:       "http",
		host:         addr,
		path:         "/xhttp/",
		mode:         mode,
		headers:      make(http.Header),
		paddingRange: intRange{0, 0},
		meta:         meta,
	}
}

// TestStreamOneConnSurvivesDialContextExpiry: a raised stream-one conn must
// outlive its dial context's deadline. Red on the pre-fix base: the request
// rides the dial context, so http2 aborts the live stream the moment the
// deadline fires — with the WG bind's C.TCPTimeout dial bound (SPEC 071) that
// tore down every healthy detour conn 15 s after connect, and the endpoint
// reconnected in a permanent cycle.
func TestStreamOneConnSurvivesDialContextExpiry(t *testing.T) {
	t.Parallel()
	addr := echoServer(t)
	client := liveH2CClient(t, addr, modeStreamOne)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dialCancel()
	conn, err := client.DialContext(dialCtx)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	// Raise the stream and verify it is live before the deadline (the server
	// echoes the uplink).
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("Write before deadline: %v", err)
	}
	banner := make([]byte, 5)
	if _, err := io.ReadFull(conn, banner); err != nil {
		t.Fatalf("Read before deadline: %v", err)
	}

	// Let the dial deadline fire, then prove the conn is still alive.
	<-dialCtx.Done()
	time.Sleep(200 * time.Millisecond) // give an (incorrect) abort time to land

	if _, err := conn.Write([]byte("after deadline")); err != nil {
		t.Fatalf("Write after dial deadline: %v — dial ctx still bounds the live stream", err)
	}
	deadlineConn, _ := conn.(interface{ SetReadDeadline(time.Time) error })
	deadlineConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	probe := make([]byte, 1)
	if _, err := conn.Read(probe); err != nil {
		t.Fatalf("Read after dial deadline: %v — dial ctx still bounds the live stream", err)
	}
}

// TestPacketUpPostsSurviveDialContextExpiry: packet-up posts must not inherit
// the dial context either — every upload POST after the dial deadline failed
// with context deadline exceeded on the pre-fix base.
func TestPacketUpPostsSurviveDialContextExpiry(t *testing.T) {
	t.Parallel()
	addr := echoServer(t)
	client := liveH2CClient(t, addr, modePacketUp)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dialCancel()
	conn, err := client.DialContext(dialCtx)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("first post")); err != nil {
		t.Fatalf("Write before deadline: %v", err)
	}
	<-dialCtx.Done()
	time.Sleep(200 * time.Millisecond)
	if _, err := conn.Write([]byte("post after deadline")); err != nil {
		t.Fatalf("Write after dial deadline: %v — posts still ride the dial ctx", err)
	}
}

// TestPacketUpPostBounded: a single upload POST must be bounded even though
// posts no longer ride the (possibly bounded) dial context — a wedged pooled
// connection costs one post timeout, not a forever-blocked Write inside the
// WG send path.
func TestPacketUpPostBounded(t *testing.T) {
	// NOT parallel: overrides packetUpPostTimeout, and a write to the package
	// var must not race the parallel tests that read it.
	restore := packetUpPostTimeout
	packetUpPostTimeout = 500 * time.Millisecond
	t.Cleanup(func() { packetUpPostTimeout = restore })

	hang := &hangRoundTripper{}
	client := stubClient(t, modePacketUp, hang)
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	select {
	case err := <-writeUnderTest(conn, []byte("payload")):
		if err == nil {
			t.Fatal("Write succeeded against a wedged pooled connection")
		}
	case <-time.After(writeFreeBudget):
		t.Fatal("Write not bounded: post still blocked on a wedged pooled connection")
	}
}

// TestPacketUpCloseAbortsPendingDownload: closing a packet-up conn must abort
// the pending download RoundTrip. Pre-fix that teardown belonged to the dial
// context ("the pending RoundTrip is torn down via the dial context instead"),
// which an unbounded dial context never fires — the RoundTrip goroutine
// leaked until box shutdown.
func TestPacketUpCloseAbortsPendingDownload(t *testing.T) {
	t.Parallel()
	hang := &hangRoundTripper{}
	client := stubClient(t, modePacketUp, hang)
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}

	conn.Close()
	deadline := time.Now().Add(writeFreeBudget)
	for hang.observed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending download RoundTrip not aborted by Close — its context never died")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
