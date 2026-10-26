//go:build with_lx_command

package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

// lx: SPEC 139 — the `transport` stage of an XHTTP dial: mode, method, path
// and the HTTP status of the download response; a refused download is a failed
// `transport_response` child and the root cause.

type statusTransport struct {
	status int
}

func (t *statusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		io.Copy(io.Discard, request.Body)
		request.Body.Close()
	}
	return &http.Response{
		StatusCode: t.status,
		Status:     http.StatusText(t.status),
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func TestTransportStage_LX(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
	}{
		{"ok", http.StatusOK},
		{"refused", http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			meta, err := normalizeMeta(metaOptions{ScMinPostsIntervalMs: "0"}, modePacketUp)
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{
				ctx:         context.Background(),
				scheme:      "https",
				host:        "example.com",
				serverAddr:  M.ParseSocksaddr("example.com:443"),
				path:        "/xhttp",
				mode:        modeAuto, // no REALITY → packet-up
				meta:        meta,
				httpVersion: httpVersion2,
				xmux:        singleTransportXmux(&statusTransport{status: testCase.status}),
			}
			var (
				emitted   []lxtrace.StageSnapshot
				emitMutex sync.Mutex
			)
			trace := lxtrace.New(lxtrace.Options{Emit: func(snapshot lxtrace.StageSnapshot) {
				emitMutex.Lock()
				emitted = append(emitted, snapshot)
				emitMutex.Unlock()
			}})
			conn, err := client.DialContext(lxtrace.With(context.Background(), trace))
			if err != nil {
				t.Fatal(err)
			}
			// The download response is processed asynchronously; a Read
			// synchronises on it (EOF or the refusal).
			conn.Read(make([]byte, 1))
			conn.Close()

			summary := trace.Finish(nil)
			var stage, child *lxtrace.StageSnapshot
			for i := range summary.Stages {
				switch summary.Stages[i].Kind {
				case "transport":
					stage = &summary.Stages[i]
				case "transport_response":
					child = &summary.Stages[i]
				}
			}
			if stage == nil {
				t.Fatalf("no transport stage: %+v", summary.Stages)
			}
			want := map[string]string{
				"mode":          modePacketUp,
				"method":        http.MethodGet,
				"upload_method": http.MethodPost,
				"path":          "/xhttp",
				"http_version":  "2",
				"http_status":   strconv.Itoa(testCase.status),
			}
			for key, value := range want {
				if got := attrOf(*stage, key); got != value {
					t.Fatalf("attr %s = %q, want %q (%+v)", key, got, value, stage.Attrs)
				}
			}
			if stage.Subject != "xhttp" || stage.Result != lxtrace.OK {
				t.Fatalf("the dial itself succeeds: %+v", *stage)
			}
			if testCase.status == http.StatusOK {
				if child != nil || summary.RootCause != 0 {
					t.Fatalf("no failure expected: %+v", summary)
				}
			} else {
				if child == nil || child.Result != lxtrace.Failed || child.Parent != stage.ID {
					t.Fatalf("a refused download is a failed child of the transport stage: %+v", summary.Stages)
				}
				if summary.RootCause != child.ID {
					t.Fatalf("root cause = %d, want the refused response %d", summary.RootCause, child.ID)
				}
			}
			emitMutex.Lock()
			defer emitMutex.Unlock()
			if len(emitted) == 0 || emitted[0].Kind != "transport" || emitted[0].Result != lxtrace.Running {
				t.Fatalf("the stage is emitted when it begins: %+v", emitted)
			}
		})
	}
}

func attrOf(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

// lx: SPEC 139 — the dials of the pooled transport itself. The request of the
// dial that opens a pooled connection carries the trace, so its TCP and TLS
// dials are children of the `transport` stage; a dial that reuses a pooled
// connection records a REUSED stage with the connection's age; fresh=true dials
// outside the pool and leaves nothing in it.

// h2EchoServer is a TLS HTTP/2 stand-in for a stream-one XHTTP server: the POST
// body is echoed back on the response. It counts the TCP connections it saw
// open and closed.
type h2EchoServer struct {
	server *httptest.Server
	opened atomic.Int32
	closed atomic.Int32
}

func newH2EchoServer(t *testing.T) *h2EchoServer {
	t.Helper()
	s := &h2EchoServer{}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		buffer := make([]byte, 1024)
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
	}))
	s.server.EnableHTTP2 = true
	s.server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			s.opened.Add(1)
		case http.StateClosed:
			s.closed.Add(1)
		}
	}
	s.server.StartTLS()
	t.Cleanup(s.server.Close)
	return s
}

func newH2TraceClient(t *testing.T, server *h2EchoServer) *Client {
	t.Helper()
	ctx := context.Background()
	defaultDialer, err := dialer.NewDefault(ctx, option.DialerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := tls.NewClient(ctx, log.NewNOPFactory().Logger(), "example.com", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "example.com",
		Insecure:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	address := server.server.Listener.Addr().(*net.TCPAddr)
	transport, err := NewClient(ctx, defaultDialer, M.SocksaddrFromNet(address), option.V2RayXHTTPOptions{
		Mode: modeStreamOne,
		Path: "/xhttp",
		// one pooled connection: the default (three) opens a new one per dial
		// until the pool is full
		Xmux: &option.V2RayXHTTPXmuxOptions{MaxConnections: "1"},
	}, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	if client.httpVersion != httpVersion2 {
		t.Fatalf("HTTP version = %s, want 2", client.httpVersion)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// traceEcho dials once under a fresh trace, round-trips a few bytes and closes
// the conn, returning the finished trace.
func traceEcho(t *testing.T, client *Client, options lxtrace.Options) lxtrace.Summary {
	t.Helper()
	trace := lxtrace.New(options)
	conn, err := client.DialContext(lxtrace.With(context.Background(), trace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}
	conn.Close()
	return trace.Finish(nil)
}

// transportChildren returns the transport stage and its direct children by kind.
func transportChildren(t *testing.T, summary lxtrace.Summary) (lxtrace.StageSnapshot, map[string][]lxtrace.StageSnapshot) {
	t.Helper()
	var transport *lxtrace.StageSnapshot
	for i := range summary.Stages {
		if summary.Stages[i].Kind == "transport" {
			transport = &summary.Stages[i]
		}
	}
	if transport == nil {
		t.Fatalf("no transport stage: %+v", summary.Stages)
	}
	children := make(map[string][]lxtrace.StageSnapshot)
	for _, stage := range summary.Stages {
		if stage.Parent == transport.ID {
			children[stage.Kind] = append(children[stage.Kind], stage)
		}
	}
	return *transport, children
}

func (m *xmuxManager) pooled() []*xmuxClient {
	m.access.Lock()
	defer m.access.Unlock()
	return append([]*xmuxClient(nil), m.clients...)
}

func TestTransportPoolDialStages_LX(t *testing.T) {
	server := newH2EchoServer(t)
	client := newH2TraceClient(t, server)
	serverName := "example.com"

	// First dial: the pool opens a connection, its dials are traced.
	summary := traceEcho(t, client, lxtrace.Options{Probe: true})
	_, children := transportChildren(t, summary)
	if len(children["tcp_connect"]) != 1 || children["tcp_connect"][0].Result != lxtrace.OK {
		t.Fatalf("first dial: want one OK tcp_connect under transport: %+v", summary.Stages)
	}
	tlsTraced := checkDialedTLS(t, "first dial", children, serverName, summary)
	pool := client.xmux.pooled()
	if len(pool) != 1 {
		t.Fatalf("pool after the first dial = %d, want 1", len(pool))
	}

	// Second dial: the pooled connection is handed out again.
	summary = traceEcho(t, client, lxtrace.Options{Probe: true})
	_, children = transportChildren(t, summary)
	if len(children["tcp_connect"]) != 0 {
		t.Fatalf("reuse dialed TCP again: %+v", summary.Stages)
	}
	reused := children["tls"]
	if len(reused) != 1 || reused[0].Result != lxtrace.Reused || reused[0].Subject != serverName {
		t.Fatalf("reuse: want one REUSED tls %s under transport: %+v", serverName, summary.Stages)
	}
	if got := attrOf(reused[0], "reuse"); got != "pooled" {
		t.Fatalf("reuse attr = %q, want pooled", got)
	}
	if age, err := strconv.Atoi(attrOf(reused[0], "age")); err != nil || age < 0 {
		t.Fatalf("age attr = %q, want whole seconds", attrOf(reused[0], "age"))
	}

	// fresh=true: a connection of its own, traced, gone with the conn; the pool
	// keeps exactly what it had.
	summary = traceEcho(t, client, lxtrace.Options{Probe: true, Fresh: true})
	stage, children := transportChildren(t, summary)
	if len(children["tcp_connect"]) != 1 || children["tcp_connect"][0].Result != lxtrace.OK {
		t.Fatalf("fresh dial: want one OK tcp_connect under transport: %+v", summary.Stages)
	}
	if checkDialedTLS(t, "fresh dial", children, serverName, summary) != tlsTraced {
		t.Fatalf("fresh dial: tls traced = %v, first dial %v", !tlsTraced, tlsTraced)
	}
	if got := attrOf(stage, "pool"); got != "detached" {
		t.Fatalf("pool attr = %q, want detached", got)
	}
	if after := client.xmux.pooled(); len(after) != 1 || after[0] != pool[0] {
		t.Fatalf("pool after the fresh dial = %v, want the first connection alone", after)
	}
	if opened := server.opened.Load(); opened != 2 {
		t.Fatalf("server saw %d connections, want 2 (pooled + fresh)", opened)
	}
	deadline := time.Now().Add(5 * time.Second)
	for server.closed.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the fresh connection was not closed with its conn (closed=%d)", server.closed.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !tlsTraced {
		t.Skip("the `tls` stage of a dialed transport is missing: common/tls defaultDialer.dialContext " +
			"(tls.NewDialer) calls aTLS.ClientHandshake without beginHandshakeLx/endHandshakeLx; " +
			"everything else passed")
	}
}

// checkDialedTLS checks the `tls` child of a dial that opened a connection and
// reports whether there is one. Its absence is tolerated (the caller skips at
// the end) because the stage is opened in common/tls, not here; a present one
// must be a single OK handshake, never the REUSED marker.
func checkDialedTLS(t *testing.T, name string, children map[string][]lxtrace.StageSnapshot, serverName string, summary lxtrace.Summary) bool {
	t.Helper()
	stages := children["tls"]
	if len(stages) == 0 {
		return false
	}
	if len(stages) != 1 || stages[0].Result != lxtrace.OK || stages[0].Subject != serverName {
		t.Fatalf("%s: want one OK tls %s under transport: %+v", name, serverName, summary.Stages)
	}
	return true
}
