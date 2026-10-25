package v2rayxhttp

import (
	"context"
	stdTLS "crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type downloadSeen struct {
	method, host, path, query, marker string
	proto                             int
}

// Supplied by the QUIC-tagged test file; absent builds exercise H1/H2 only.
var newHTTP3DownloadServer func(*testing.T, http.Handler) (M.Socksaddr, *option.OutboundTLSOptions)

func downloadTestEndpoint(t *testing.T, version int, handler http.Handler) (M.Socksaddr, *option.OutboundTLSOptions) {
	if version == 3 {
		return newHTTP3DownloadServer(t, handler)
	}
	server, config := downloadTestServer(t, version == 2, handler)
	return M.SocksaddrFromNet(server.Listener.Addr()), config
}

func downloadTestServer(t *testing.T, h2 bool, handler http.Handler) (*httptest.Server, *option.OutboundTLSOptions) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	if !h2 {
		server.Start()
		t.Cleanup(server.Close)
		return server, nil
	}
	certificate, err := tls.GenerateKeyPair(nil, nil, time.Now, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	server.EnableHTTP2 = true
	server.TLS = &stdTLS.Config{Certificates: []stdTLS.Certificate{*certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	return server, &option.OutboundTLSOptions{Enabled: true, ServerName: "localhost", Certificate: []string{string(roots)}, ALPN: []string{"h2"}}
}

func assertNoDownloadLeases(t *testing.T, client *Client) {
	t.Helper()
	for _, manager := range []*xmuxManager{client.xmux, client.download.xmux} {
		manager.access.Lock()
		for _, pooled := range manager.clients {
			pooled.access.Lock()
			usage := pooled.openUsage
			pooled.access.Unlock()
			if usage != 0 {
				manager.access.Unlock()
				t.Fatalf("retained XMUX usage after close: %d", usage)
			}
		}
		manager.access.Unlock()
	}
}

func TestIndependentDownloadEndpoints(t *testing.T) {
	pairs := [][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}}
	if newHTTP3DownloadServer != nil {
		pairs = append(pairs, [2]int{1, 3}, [2]int{2, 3}, [2]int{3, 1}, [2]int{3, 2}, [2]int{3, 3})
	}
	for _, mode := range []string{modeAuto, modePacketUp, modeStreamUp} {
		for _, versions := range pairs {
			name := fmt.Sprintf("%s/h%d-h%d", mode, versions[0], versions[1])
			t.Run(name, func(t *testing.T) {
				data := make(chan []byte, 1)
				seenUp, seenDown := make(chan downloadSeen, 1), make(chan downloadSeen, 1)
				record := func(r *http.Request) downloadSeen {
					return downloadSeen{r.Method, r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Leg"), r.ProtoMajor}
				}
				upAddr, upTLS := downloadTestEndpoint(t, versions[0], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seenUp <- record(r)
					http.NewResponseController(w).EnableFullDuplex()
					buffer := make([]byte, 5)
					if _, err := io.ReadFull(r.Body, buffer); err != nil {
						return
					}
					data <- buffer
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					io.Copy(io.Discard, r.Body)
				}))
				downAddr, downTLS := downloadTestEndpoint(t, versions[1], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seenDown <- record(r)
					select {
					case payload := <-data:
						w.Write(payload)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					case <-r.Context().Done():
					}
				}))
				var upConfig tls.Config
				if upTLS != nil {
					var err error
					upConfig, err = tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", *upTLS)
					if err != nil {
						t.Fatal(err)
					}
				}
				options := option.V2RayXHTTPOptions{Mode: mode, Host: "upload.example", Path: "/up/?u=1"}
				options.Headers = badoption.HTTPHeader{"X-Leg": {"up"}}
				options.DownloadSettings = &option.V2RayXHTTPDownloadOptions{
					ServerOptions:               option.ServerOptions{Server: downAddr.AddrString(), ServerPort: downAddr.Port},
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: downTLS},
					XHTTP:                       option.V2RayXHTTPOptions{Host: "download.example", Path: "/down/?d=2", Headers: badoption.HTTPHeader{"X-Leg": {"down"}}},
				}
				transport, err := NewClient(context.Background(), N.SystemDialer, upAddr, options, upConfig)
				if err != nil {
					t.Fatal(err)
				}
				client := transport.(*Client)
				defer client.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, err := client.DialContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err = conn.Write([]byte("hello")); err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, 5)
				if _, err = io.ReadFull(conn, buffer); err != nil || string(buffer) != "hello" {
					t.Fatalf("echo %q: %v", buffer, err)
				}
				u, d := <-seenUp, <-seenDown
				if u.method == http.MethodGet || d.method != http.MethodGet || u.host != "upload.example" || d.host != "download.example" || u.marker != "up" || d.marker != "down" {
					t.Fatalf("legs crossed: up=%+v down=%+v", u, d)
				}
				upSession := strings.Split(strings.Trim(u.path, "/"), "/")[1]
				downSession := strings.Split(strings.Trim(d.path, "/"), "/")[1]
				if upSession != downSession || !strings.HasPrefix(u.path, "/up/") || !strings.HasPrefix(d.path, "/down/") || !strings.Contains(u.query, "u=1") || !strings.Contains(d.query, "d=2") {
					t.Fatalf("different session or request metadata: up=%+v down=%+v", u, d)
				}
				if u.proto != versions[0] || d.proto != versions[1] {
					t.Fatal("HTTP version leaked between legs")
				}
				conn.Close()
				conn.Close()
				assertNoDownloadLeases(t, client)
			})
		}
	}
}

func TestDownloadOptionsRejectIncompleteOrRecursiveEndpoints(t *testing.T) {
	for _, settings := range []*option.V2RayXHTTPDownloadOptions{
		{}, {ServerOptions: option.ServerOptions{Server: "localhost"}},
		{ServerOptions: option.ServerOptions{Server: "localhost", ServerPort: 443}, XHTTP: option.V2RayXHTTPOptions{DownloadSettings: &option.V2RayXHTTPDownloadOptions{}}},
		{ServerOptions: option.ServerOptions{Server: "localhost", ServerPort: 443}, Dialer: &option.DialerOptions{Detour: "missing-manager"}},
	} {
		if client, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("localhost:443"), option.V2RayXHTTPOptions{DownloadSettings: settings}, nil); err == nil {
			client.Close()
			t.Fatal("invalid download settings accepted")
		}
	}
}

func TestDownloadOptionsRejectStreamOne(t *testing.T) {
	client, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("localhost:443"), option.V2RayXHTTPOptions{
		Mode: modeStreamOne,
		DownloadSettings: &option.V2RayXHTTPDownloadOptions{
			ServerOptions: option.ServerOptions{Server: "localhost", ServerPort: 443},
		},
	}, nil)
	if err == nil {
		client.Close()
		t.Fatal("stream-one accepted separate download settings")
	}
	if !strings.Contains(err.Error(), "incompatible with stream-one") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestDownloadTLSFailureDoesNotFallBackToUpload(t *testing.T) {
	var upGets atomic.Int32
	up, upTLS := downloadTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			upGets.Add(1)
		}
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	down, _ := downloadTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("unsafe")) }))
	upAddress := M.ParseSocksaddr(strings.TrimPrefix(up.URL, "https://"))
	downAddress := M.ParseSocksaddr(strings.TrimPrefix(down.URL, "https://"))
	upConfig, err := tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", *upTLS)
	if err != nil {
		t.Fatal(err)
	}
	// Trust only the upload certificate on the download leg: the other server
	// must fail certificate verification rather than reusing the healthy leg.
	transport, err := NewClient(context.Background(), N.SystemDialer, upAddress, option.V2RayXHTTPOptions{Mode: modePacketUp,
		DownloadSettings: &option.V2RayXHTTPDownloadOptions{ServerOptions: option.ServerOptions{Server: downAddress.AddrString(), ServerPort: downAddress.Port},
			OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: upTLS}}}, upConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	defer client.Close()
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err = conn.Read(make([]byte, 6)); err == nil {
		t.Fatal("untrusted download certificate accepted")
	}
	conn.Close()
	if upGets.Load() != 0 {
		t.Fatal("download silently fell back to upload endpoint")
	}
	assertNoDownloadLeases(t, client)
}

func TestCancelledDownloadAcquisitionReleasesUpload(t *testing.T) {
	transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:1"), option.V2RayXHTTPOptions{Mode: modeStreamUp,
		DownloadSettings: &option.V2RayXHTTPDownloadOptions{ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 2}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	defer client.Close()
	client.download.xmux.blockedUntil = time.Now().Add(time.Hour)
	client.download.xmux.backoffDelay = time.Hour
	client.download.xmux.backoffArmed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if conn, err := client.DialContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("blocked download acquisition = %v", err)
	}
	assertNoDownloadLeases(t, client)
}

func TestDownloadReadHealthUsesSeparatePool(t *testing.T) {
	up, down := &xmuxClient{}, &xmuxClient{}
	up.addOpenUsage(1)
	down.addOpenUsage(1)
	lease := newXmuxRelease(up)
	lease.peer = newXmuxRelease(down)
	if lease.readSide().client != down {
		t.Fatal("read health charged to upload")
	}
	lease.release()
	lease.release()
	lease.peer.release()
	up.access.Lock()
	upUsage := up.openUsage
	up.access.Unlock()
	down.access.Lock()
	downUsage := down.openUsage
	down.access.Unlock()
	if upUsage != 0 || downUsage != 0 {
		t.Fatalf("double/missing release: %d/%d", upUsage, downUsage)
	}
}

func TestDownloadUploadFailureReleasesBothPools(t *testing.T) {
	up, down := &xmuxClient{}, &xmuxClient{}
	up.addOpenUsage(1)
	down.addOpenUsage(1)
	lease := newXmuxRelease(up)
	lease.peer = newXmuxRelease(down)
	r, w := io.Pipe()
	conn := newSplitConn(r, w, M.ParseSocksaddr("localhost:443"), lease)
	ctx, cancel := context.WithCancel(context.Background())
	conn.cancel = cancel
	cause := errors.New("upload failed")
	conn.uploadFailed(cause)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("failed upload left download request alive")
	}
	if _, err := conn.Write([]byte("orphan")); !errors.Is(err, cause) {
		t.Fatalf("upload cause lost: %v", err)
	}
	if !conn.breaker.localClosed.Load() {
		t.Fatal("upload failure charged teardown to download health")
	}
	conn.Close()
	up.access.Lock()
	upUsage := up.openUsage
	up.access.Unlock()
	down.access.Lock()
	downUsage := down.openUsage
	down.access.Unlock()
	if upUsage != 0 || downUsage != 0 {
		t.Fatalf("upload failure retained leases: %d/%d", upUsage, downUsage)
	}
}
