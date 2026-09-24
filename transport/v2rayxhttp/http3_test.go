//go:build with_xhttp && with_quic

package v2rayxhttp

import (
	"context"
	"crypto/sha256"
	stdTLS "crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/badh2"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
)

func testCertificate(t *testing.T) (*stdTLS.Certificate, []byte) {
	t.Helper()
	certificate, err := tls.GenerateKeyPair(nil, nil, time.Now, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(publicKey)
	return certificate, hash[:]
}

func TestHTTP3KeepAlivePeriod(t *testing.T) {
	for _, testCase := range []struct {
		in, want time.Duration
	}{
		{0, 10 * time.Second},
		{25 * time.Second, 25 * time.Second},
		{-time.Second, 0},
	} {
		newConn, err := newHTTP3Transport(N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), testTLSConfig(t, "h3"), testCase.in, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		conn := newConn().(*http3XmuxConn)
		quicConfig := conn.transport.QUICConfig
		if quicConfig.KeepAlivePeriod != testCase.want {
			t.Fatalf("KeepAlivePeriod(%v) = %v, want %v", testCase.in, quicConfig.KeepAlivePeriod, testCase.want)
		}
		if quicConfig.MaxIdleTimeout != 300*time.Second || quicConfig.MaxIncomingStreams != -1 || !quicConfig.ChromeParrot {
			t.Fatalf("quic.Config differs from Xray's: %+v", quicConfig)
		}
		if nextProtos := conn.transport.TLSClientConfig.NextProtos; len(nextProtos) != 1 || nextProtos[0] != "h3" {
			t.Fatalf("NextProtos = %v", nextProtos)
		}
		conn.Close()
	}
}

// TestHTTP3XmuxConnIsClosed: evicted after our Close and once the QUIC
// connection it dialed is gone.
func TestHTTP3XmuxConnIsClosed(t *testing.T) {
	newConn, err := newHTTP3Transport(N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), testTLSConfig(t, "h3"), 0, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	closedByUs := newConn().(*http3XmuxConn)
	if closedByUs.IsClosed() {
		t.Fatal("fresh connection reports closed")
	}
	closedByUs.Close()
	if !closedByUs.IsClosed() {
		t.Fatal("IsClosed after Close = false")
	}

	certificate, _ := testCertificate(t)
	listener, err := quic.ListenAddr("127.0.0.1:0", &stdTLS.Config{Certificates: []stdTLS.Certificate{*certificate}, NextProtos: []string{"h3"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			defer conn.CloseWithError(0, "")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	quicConn, err := quic.DialAddr(ctx, listener.Addr().String(), &stdTLS.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	died := newConn().(*http3XmuxConn)
	defer died.Close()
	died.conn.Store(quicConn)
	if died.IsClosed() {
		t.Fatal("live QUIC connection reports closed")
	}
	quicConn.CloseWithError(0, "")
	select {
	case <-quicConn.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("QUIC context not done")
	}
	if !died.IsClosed() {
		t.Fatal("IsClosed after the QUIC connection died = false")
	}
}

// TestHideQUICError: quic-go / http3 types do not leave the conn, keep their
// text, lose Timeout()/Temporary(), and connection-level ones still read as
// net.ErrClosed (SPEC 104 §7).
func TestHideQUICError(t *testing.T) {
	type timeout interface{ Timeout() bool }
	type temporary interface{ Temporary() bool }
	cases := []struct {
		err    error
		closed bool
	}{
		{&http3.Error{Remote: true, ErrorCode: http3.ErrCodeInternalError}, false},
		{&quic.StreamError{StreamID: 4, ErrorCode: 1, Remote: true}, false},
		{&quic.IdleTimeoutError{}, true},
		{&quic.HandshakeTimeoutError{}, true},
		{&quic.StatelessResetError{}, true},
		{&quic.TransportError{ErrorCode: quic.InternalError, Remote: true}, true},
		{&quic.ApplicationError{ErrorCode: 0x100, Remote: true}, true},
	}
	for _, testCase := range cases {
		hidden := hideTransportError(testCase.err)
		if hidden.Error() != testCase.err.Error() {
			t.Fatalf("%T: text %q, want %q", testCase.err, hidden.Error(), testCase.err.Error())
		}
		if _, isHidden := hidden.(*quicHiddenError); !isHidden {
			t.Fatalf("%T leaked as %T", testCase.err, hidden)
		}
		var (
			h3Error          *http3.Error
			streamError      *quic.StreamError
			idleTimeoutError *quic.IdleTimeoutError
			resetError       *quic.StatelessResetError
		)
		if errors.As(hidden, &h3Error) || errors.As(hidden, &streamError) || errors.As(hidden, &idleTimeoutError) || errors.As(hidden, &resetError) {
			t.Fatalf("%T reachable through errors.As", testCase.err)
		}
		if value, ok := hidden.(timeout); ok && value.Timeout() {
			t.Fatalf("%T: Timeout() = true", testCase.err)
		}
		if value, ok := hidden.(temporary); ok && value.Temporary() {
			t.Fatalf("%T: Temporary() = true", testCase.err)
		}
		if got := errors.Is(hidden, net.ErrClosed); got != testCase.closed {
			t.Fatalf("%T: errors.Is(net.ErrClosed) = %v, want %v", testCase.err, got, testCase.closed)
		}
	}
	if hideTransportError(io.EOF) != io.EOF || hideTransportError(nil) != nil {
		t.Fatal("io.EOF / nil must pass through")
	}
	var streamError http2.StreamError
	if errors.As(hideTransportError(http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}), &streamError) {
		t.Fatal("http2.StreamError leaked")
	}
	if _, isRemote := hideTransportError(http2.StreamError{StreamID: 1}).(*badh2.RemoteStreamError); !isRemote {
		t.Fatal("http2.StreamError not hidden by badh2")
	}
}

// TestHTTP3BreakerClassification: our cancel is neutral, a remote stream reset
// counts, and a locally closed body reads as net.ErrClosed.
func TestHTTP3BreakerClassification(t *testing.T) {
	transport := &failingRT{errs: []error{context.Canceled, &http3.Error{Remote: true, ErrorCode: http3.ErrCodeInternalError}}}
	manager := singleTransportXmux(transport)
	client, _ := manager.get()
	client.roundTrip(testRequest(t))
	if fails := client.consecFails.Load(); fails != 0 {
		t.Fatalf("context.Canceled counted: consecFails = %d", fails)
	}
	client.roundTrip(testRequest(t))
	if fails := client.consecFails.Load(); fails != 1 {
		t.Fatalf("remote http3.Error not counted: consecFails = %d", fails)
	}

	var breaker connBreaker
	breaker.localClosed.Store(true)
	localClose := &http3.Error{Remote: false, ErrorCode: http3.ErrCodeRequestCanceled}
	if got := breaker.outboundErr(hideTransportError(localClose), nil); got != net.ErrClosed {
		t.Fatalf("local close = %v, want net.ErrClosed", got)
	}
}

// TestHTTP3StreamOneRoundTrip: a stream-one exchange over HTTP/3 against an
// in-process http3 server, certificate pinned by public key.
func TestHTTP3StreamOneRoundTrip(t *testing.T) {
	certificate, hash := testCertificate(t)
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&stdTLS.Config{Certificates: []stdTLS.Certificate{*certificate}}),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			buffer := make([]byte, 4)
			n, _ := io.ReadFull(r.Body, buffer)
			w.Write(buffer[:n])
			w.(http.Flusher).Flush()
			io.Copy(io.Discard, r.Body)
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
	transport, err := NewClient(context.Background(), N.SystemDialer, M.SocksaddrFromNet(packetConn.LocalAddr()), option.V2RayXHTTPOptions{Mode: modeStreamOne, Path: "/xhttp/"}, tlsConfig)
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
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}
}
