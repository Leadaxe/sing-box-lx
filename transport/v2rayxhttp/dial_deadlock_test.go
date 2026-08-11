package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// xrayLikeServer models the half of the XHTTP server contract that made
// packet-up/stream-up deadlock: the stream-down response is withheld until the
// paired uplink for the same session has been received. Xray behaves this way
// because it has nothing to send downstream before the session carries traffic;
// a reverse proxy in front (nginx/CDN) turns the resulting stall into a 504.
type xrayLikeServer struct {
	uplinkSeen chan struct{} // closed by the first upload request
	server     *http.Server
	addr       string
}

func newXrayLikeServer(t *testing.T) *xrayLikeServer {
	t.Helper()
	s := &xrayLikeServer{uplinkSeen: make(chan struct{})}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An upload carries either a seq (packet-up) or a request body
		// (stream-up, where the streamed body makes ContentLength -1/chunked).
		isUpload := r.URL.Query().Get("chunk_id") != "" || r.ContentLength != 0 || r.Method != http.MethodGet
		if isUpload {
			// Read just the first chunk: a stream-up body never ends, so
			// draining it fully would hang the handler before it can signal.
			buffer := make([]byte, 1)
			r.Body.Read(buffer)
			select {
			case <-s.uplinkSeen:
			default:
				close(s.uplinkSeen)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		// Download: withhold the response until an uplink shows up. This is the
		// exact ordering the old synchronous dial could never satisfy.
		select {
		case <-s.uplinkSeen:
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			// Stand-in for the fronting proxy's upstream timeout.
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		w.Write([]byte("downlink"))
		w.(http.Flusher).Flush()
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.addr = listener.Addr().String()
	s.server = &http.Server{Handler: h2c.NewHandler(handler, &http2.Server{})}
	go s.server.Serve(listener)
	t.Cleanup(func() { s.server.Close() })
	return s
}

// h2cClient builds a Client speaking cleartext HTTP/2 at addr, shaped like the
// CDN-VK config that exposed the deadlock: packet-up, session in a header, seq
// in a query parameter.
func h2cClient(t *testing.T, addr, mode string) *Client {
	t.Helper()
	meta, err := normalizeMeta(metaOptions{
		SessionPlacement: placementHeader,
		SessionKey:       "X-Upload-Token",
		SeqPlacement:     placementQuery,
		SeqKey:           "chunk_id",
	}, mode)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	return &Client{
		ctx:        context.Background(),
		serverAddr: M.ParseSocksaddr(addr),
		transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		scheme:       "http",
		host:         addr,
		path:         "/upload/",
		mode:         mode,
		headers:      make(http.Header),
		paddingRange: intRange{0, 0},
		meta:         meta,
	}
}

// TestDialDoesNotBlockOnDownloadResponse is the regression guard for the
// packet-up / stream-up deadlock. Against a server that answers stream-down only
// after the first uplink, a dial that waits for the download response before
// letting the caller write can never complete — it stalls until the fronting
// proxy returns 504. The dial must hand the conn up immediately so the first
// Write can unblock the download.
func TestDialDoesNotBlockOnDownloadResponse(t *testing.T) {
	for _, mode := range []string{modePacketUp, modeStreamUp} {
		t.Run(mode, func(t *testing.T) {
			server := newXrayLikeServer(t)
			client := h2cClient(t, server.addr, mode)

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			dialed := make(chan net.Conn, 1)
			dialErr := make(chan error, 1)
			go func() {
				conn, err := client.DialContext(ctx)
				if err != nil {
					dialErr <- err
					return
				}
				dialed <- conn
			}()

			var conn net.Conn
			select {
			case conn = <-dialed:
			case err := <-dialErr:
				t.Fatalf("dial failed: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("dial blocked waiting for the download response — the deadlock is back")
			}
			defer conn.Close()

			// The write is what releases the server's download response.
			if _, err := conn.Write([]byte("uplink")); err != nil {
				t.Fatalf("write: %v", err)
			}

			read := make(chan error, 1)
			go func() {
				buffer := make([]byte, len("downlink"))
				_, err := io.ReadFull(conn, buffer)
				read <- err
			}()
			select {
			case err := <-read:
				if err != nil {
					t.Fatalf("read downlink: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("downlink never arrived after the uplink")
			}
		})
	}
}
