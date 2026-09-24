package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type http1Seen struct {
	method   string
	close    bool
	streamed bool // chunked body of unknown length (stream-one / stream-up)
	remote   string
	proto    int
}

// http1Server is a plain HTTP/1.1 XHTTP stand-in (httptest, no h2c): GETs get
// a flushed downlink body, streamed POSTs get their bytes echoed, packet-up
// POSTs are drained and answered 200.
type http1Server struct {
	server *httptest.Server
	access sync.Mutex
	seen   []http1Seen
}

func newHTTP1Server(t *testing.T) *http1Server {
	t.Helper()
	s := &http1Server{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamed := r.Method != http.MethodGet && r.ContentLength < 0
		s.access.Lock()
		s.seen = append(s.seen, http1Seen{method: r.Method, close: r.Close, streamed: streamed, remote: r.RemoteAddr, proto: r.ProtoMajor})
		s.access.Unlock()
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("down"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case streamed:
			// Xray reads the uplink while it writes the downlink.
			http.NewResponseController(w).EnableFullDuplex()
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			buffer := make([]byte, 4)
			n, _ := io.ReadFull(r.Body, buffer)
			w.Write(buffer[:n])
			w.(http.Flusher).Flush()
			// A full-duplex handler sees the client leave only through the body.
			io.Copy(io.Discard, r.Body)
		default:
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *http1Server) requests() []http1Seen {
	s.access.Lock()
	defer s.access.Unlock()
	return append([]http1Seen(nil), s.seen...)
}

func (s *http1Server) waitRequests(t *testing.T, n int) []http1Seen {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if seen := s.requests(); len(seen) >= n {
			return seen
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server saw %d requests, want %d", len(s.requests()), n)
	return nil
}

func newHTTP1TestClient(t *testing.T, server *http1Server, mode string) *Client {
	t.Helper()
	address := server.server.Listener.Addr().(*net.TCPAddr)
	transport, err := NewClient(context.Background(), N.SystemDialer, M.SocksaddrFromNet(address), option.V2RayXHTTPOptions{
		Mode: mode,
		Path: "/xhttp/",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	t.Cleanup(func() { client.Close() })
	return client
}

// TestHTTP1PacketUp: the download GET goes with Connection: close, the upload
// POSTs reuse one keep-alive connection (SPEC 104 §5.2).
func TestHTTP1PacketUp(t *testing.T) {
	server := newHTTP1Server(t)
	client := newHTTP1TestClient(t, server, modePacketUp)
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for range 3 {
		if _, err := conn.Write([]byte("up")); err != nil {
			t.Fatal(err)
		}
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "down" {
		t.Fatalf("read = %q, %v", buffer, err)
	}
	seen := server.waitRequests(t, 4)
	var posts []http1Seen
	for _, request := range seen {
		if request.proto != 1 {
			t.Fatalf("request went HTTP/%d", request.proto)
		}
		switch request.method {
		case http.MethodGet:
			if !request.close {
				t.Fatal("download GET without Connection: close")
			}
		case http.MethodPost:
			if request.close {
				t.Fatal("upload POST with Connection: close")
			}
			posts = append(posts, request)
		}
	}
	if len(posts) != 3 {
		t.Fatalf("saw %d upload POSTs, want 3", len(posts))
	}
	for _, post := range posts[1:] {
		if post.remote != posts[0].remote {
			t.Fatalf("upload POSTs did not reuse the connection: %s vs %s", post.remote, posts[0].remote)
		}
	}
}

// TestHTTP1StreamModes: every long request (download GET, streamed POST) is
// its own Connection: close request.
func TestHTTP1StreamModes(t *testing.T) {
	for _, testCase := range []struct {
		mode     string
		requests int
	}{
		{modeStreamUp, 2},
		{modeStreamOne, 1},
	} {
		t.Run(testCase.mode, func(t *testing.T) {
			server := newHTTP1Server(t)
			client := newHTTP1TestClient(t, server, testCase.mode)
			conn, err := client.DialContext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			seen := server.waitRequests(t, testCase.requests)
			remotes := make(map[string]bool)
			for _, request := range seen {
				if !request.close {
					t.Fatalf("%s without Connection: close", request.method)
				}
				if request.proto != 1 {
					t.Fatalf("request went HTTP/%d", request.proto)
				}
				remotes[request.remote] = true
			}
			if len(remotes) != testCase.requests {
				t.Fatalf("%d requests shared %d connections", testCase.requests, len(remotes))
			}
			if testCase.mode == modeStreamOne {
				buffer := make([]byte, 4)
				if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
					t.Fatalf("echo = %q, %v", buffer, err)
				}
			}
		})
	}
}
