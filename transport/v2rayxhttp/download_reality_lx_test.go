//go:build with_xhttp && with_utls

package v2rayxhttp

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	stdTLS "crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/http2"
)

var downloadRealityNames atomic.Uint64

func init() { newRealityDownloadServer = downloadRealityEndpoint }

// Each leg has its own ephemeral key, short ID, SNI and loopback camouflage
// target. These native REALITY frontends do not claim direct Xray REALITY ingress.
func downloadRealityEndpoint(t *testing.T, handler http.Handler) (M.Socksaddr, *option.OutboundTLSOptions) {
	t.Helper()
	number := downloadRealityNames.Add(1)
	name := fmt.Sprintf("leg-%d.fixture.invalid", number)
	shortID := fmt.Sprintf("%016x", number)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.GenerateKeyPair(nil, nil, time.Now, name)
	if err != nil {
		t.Fatal(err)
	}
	camouflage := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("local camouflage")) }))
	camouflage.EnableHTTP2 = true
	camouflage.TLS = &stdTLS.Config{MinVersion: stdTLS.VersionTLS13, MaxVersion: stdTLS.VersionTLS13, Certificates: []stdTLS.Certificate{*certificate}}
	camouflage.StartTLS()
	t.Cleanup(camouflage.Close)
	target := M.SocksaddrFromNet(camouflage.Listener.Addr())
	ctx, cancel := context.WithCancel(context.Background())
	config, err := tls.NewRealityServer(ctx, log.NewNOPFactory().Logger(), option.InboundTLSOptions{
		Enabled: true, ServerName: name, ALPN: []string{"h2"},
		Reality: &option.InboundRealityOptions{Enabled: true,
			PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortID: []string{shortID},
			Handshake: option.InboundRealityHandshakeOptions{ServerOptions: option.ServerOptions{Server: target.AddrString(), ServerPort: target.Port}},
		},
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	config.SetHandshakeTimeout(5 * time.Second)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var connections sync.Map
	var workers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Store(raw, true)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connections.Delete(raw)
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(10 * time.Second))
				secure, err := tls.ServerHandshake(ctx, raw, config)
				if err != nil {
					return
				}
				defer secure.Close()
				state := secure.ConnectionState()
				if !state.HandshakeComplete || state.ServerName != name || state.NegotiatedProtocol != "" && state.NegotiatedProtocol != "h2" {
					t.Errorf("REALITY handshake identity/ALPN mismatch: %q %q", state.ServerName, state.NegotiatedProtocol)
					return
				}
				server := &http2.Server{}
				// The pinned uTLS REALITY server leaves clientProtocol empty.
				// Verify HTTP/2 on parsed requests rather than inventing ALPN metadata.
				checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.ProtoMajor != 2 {
						t.Error("REALITY request was not HTTP/2")
						http.Error(w, "wrong protocol", 400)
						return
					}
					handler.ServeHTTP(w, r)
				})
				server.ServeConn(secure, &http2.ServeConnOpts{Context: ctx, Handler: checked})
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-accepted
		cancel()
		connections.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
		finished := make(chan struct{})
		go func() { workers.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("REALITY frontend connections did not close")
		}
		config.Close()
	})
	return M.SocksaddrFromNet(listener.Addr()), &option.OutboundTLSOptions{
		Enabled: true, ServerName: name, ALPN: []string{"h2"},
		UTLS: &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
		Reality: &option.OutboundRealityOptions{Enabled: true,
			PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortID: shortID, KeyShare: "classical"},
	}
}

func TestDownloadRealityKeyShareExchange(t *testing.T) {
	for _, share := range []string{"classical", "", "hybrid"} {
		label := share
		if label == "" {
			label = "fingerprint-default"
		}
		t.Run(label, func(t *testing.T) {
			address, options := downloadRealityEndpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
			options.Reality.KeyShare = share
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			config, err := tls.NewClient(ctx, log.NewNOPFactory().Logger(), options.ServerName, *options)
			if err != nil {
				t.Fatal(err)
			}
			dialer := net.Dialer{}
			raw, err := dialer.DialContext(ctx, "tcp", address.String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			secure, err := tls.ClientHandshake(ctx, raw, config)
			if err != nil {
				t.Fatal(err)
			}
			defer secure.Close()
			state := secure.ConnectionState()
			// REALITY forces H2 independently of ALPN (SPEC 104); verify parsed HTTP/2 below.
			if !state.HandshakeComplete || state.ServerName != options.ServerName {
				t.Fatalf("REALITY client handshake identity mismatch: complete=%v sni=%q alpn=%q", state.HandshakeComplete, state.ServerName, state.NegotiatedProtocol)
			}
			transport := &http2.Transport{}
			connection, err := transport.NewClientConn(secure)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+options.ServerName+"/probe", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := connection.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			payload, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || string(payload) != "hello" {
				t.Fatalf("REALITY HTTP/2 exchange failed: %v", err)
			}
		})
	}
}

func TestDownloadRealityRejectsOtherLegIdentity(t *testing.T) {
	for _, mutation := range []string{"public-key", "short-id", "sni", "upload-key"} {
		t.Run(mutation, func(t *testing.T) {
			var upGets, downRequests atomic.Int32
			up, upTLS := downloadRealityEndpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					upGets.Add(1)
					w.Write([]byte("unsafe"))
					return
				}
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			down, downTLS := downloadRealityEndpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downRequests.Add(1); w.Write([]byte("unsafe")) }))
			switch mutation {
			case "public-key":
				key, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				downTLS.Reality.PublicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
			case "short-id":
				downTLS.Reality.ShortID = "ffffffffffffffff"
			case "sni":
				downTLS.ServerName = "wrong.fixture.invalid"
			case "upload-key":
				downTLS.Reality.PublicKey = upTLS.Reality.PublicKey
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			config, err := tls.NewClient(ctx, log.NewNOPFactory().Logger(), upTLS.ServerName, *upTLS)
			if err != nil {
				t.Fatal(err)
			}
			transport, err := NewClient(ctx, N.SystemDialer, up, option.V2RayXHTTPOptions{Mode: modePacketUp,
				DownloadSettings: &option.V2RayXHTTPDownloadOptions{
					ServerOptions:               option.ServerOptions{Server: down.AddrString(), ServerPort: down.Port},
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: downTLS},
				},
			}, config)
			if err != nil {
				t.Fatal(err)
			}
			client := transport.(*Client)
			defer client.Close()
			conn, exchangeError := client.DialContext(ctx)
			if exchangeError == nil {
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, exchangeError = conn.Write([]byte("hello"))
				if exchangeError == nil {
					_, exchangeError = io.ReadFull(conn, make([]byte, 5))
				}
				conn.Close()
			}
			if exchangeError == nil {
				t.Fatal("incorrect download identity authenticated")
			}
			message := strings.ToLower(exchangeError.Error())
			if !strings.Contains(message, "certificate") && !strings.Contains(message, "reality") && !strings.Contains(message, "tls") {
				t.Fatalf("expected authentication error, got: %v", exchangeError)
			}
			if upGets.Load() != 0 || downRequests.Load() != 0 {
				t.Fatalf("wrong identity used upload fallback or reached download handler: up=%d down=%d", upGets.Load(), downRequests.Load())
			}
			assertNoDownloadLeases(t, client)
			t.Logf("incorrect %s rejected without fallback: %v", mutation, exchangeError)
		})
	}
}
