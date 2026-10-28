//go:build with_xhttp && with_quic

package v2rayxhttp

import (
	"bytes"
	"context"
	stdTLS "crypto/tls"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// SPECS/TASKS/141-XHTTP_PACKET_UP_PIPELINE — the pipeline over HTTP/3: quic-go
// fires httptrace.WroteRequest, so the next POST goes out while the previous
// response is still delayed. Same fixture as TestPacketUpLiveH2Pipelined.
func TestHTTP3PacketUpPipelined(t *testing.T) {
	const chunk = 32 * 1024
	const chunks = 16
	const delay = 50 * time.Millisecond

	certificate, hash := testCertificate(t)
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int64
	var posts atomic.Int32
	server := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&stdTLS.Config{Certificates: []stdTLS.Certificate{*certificate}}),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			posts.Add(1)
			received.Add(n)
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
	}
	go server.Serve(packetConn)
	defer server.Close()

	tlsConfig, err := tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", option.OutboundTLSOptions{
		Enabled:                    true,
		ServerName:                 "localhost",
		ALPN:                       []string{"h3"},
		CertificatePublicKeySHA256: [][]byte{hash},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewClient(context.Background(), N.SystemDialer, M.SocksaddrFromNet(packetConn.LocalAddr()), option.V2RayXHTTPOptions{
		Mode: modePacketUp, Path: "/xhttp/", ScMinPostsIntervalMs: "0", ScMaxEachPostBytes: "1000000",
	}, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	defer client.Close()
	if client.httpVersion != httpVersion3 {
		t.Fatalf("version = %s", client.httpVersion)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Wait for the download to be up so the timer measures uploads only.
	payload := bytes.Repeat([]byte{0xAB}, chunk)
	started := time.Now()
	for i := 0; i < chunks; i++ {
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(writeFreeBudget)
	for received.Load() < chunk*chunks {
		if time.Now().After(deadline) {
			t.Fatalf("server received %d of %d bytes in %d POSTs", received.Load(), chunk*chunks, posts.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed >= chunks*delay/2 {
		t.Fatalf("upload took %v over HTTP/3 — serial posts (≥ %v) rather than pipelined", elapsed, chunks*delay)
	}
}
