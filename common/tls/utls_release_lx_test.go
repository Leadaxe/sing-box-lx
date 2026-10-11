//go:build with_utls

package tls

// lx:begin SPEC 122 tests

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func lxReleaseCert(t *testing.T, name string) stdtls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return stdtls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// lxReleaseServer runs a crypto/tls server on conn that echoes, and reports the
// ALPN list of the ClientHello it received.
func lxReleaseServer(t *testing.T, conn net.Conn, maxVersion uint16, protos chan<- []string) {
	server := stdtls.Server(conn, &stdtls.Config{
		Certificates: []stdtls.Certificate{lxReleaseCert(t, "example.com")},
		NextProtos:   []string{"h2", "http/1.1"},
		MaxVersion:   maxVersion,
		GetConfigForClient: func(hello *stdtls.ClientHelloInfo) (*stdtls.Config, error) {
			if protos != nil {
				protos <- hello.SupportedProtos
			}
			return nil, nil
		},
	})
	defer server.Close()
	if server.Handshake() != nil {
		return
	}
	_, _ = io.Copy(server, server)
}

// lxReleasePair is a loopback TCP pair: unlike net.Pipe it buffers, so the
// server's post-handshake NewSessionTicket does not block the client's write.
func lxReleasePair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err = net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	server = <-accepted
	require.NotNil(t, server)
	return client, server
}

func lxReleaseClient(conn net.Conn, id utls.ClientHelloID, nextProtos []string) *lxUTLSConn {
	return newLXUTLSConn(utls.UClient(conn, &utls.Config{ServerName: "example.com", InsecureSkipVerify: true, NextProtos: nextProtos}, id), nextProtos)
}

func lxRequireReleased(t *testing.T, uConn *utls.UConn) {
	t.Helper()
	require.Nil(t, uConn.HandshakeState.Hello)
	require.Nil(t, uConn.HandshakeState.ServerHello)
	require.Nil(t, uConn.HandshakeState.State13.KeyShareKeys)
	require.Nil(t, uConn.Extensions)
}

func lxEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	_, err := conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))
}

// The wire ALPN is the transport's list (or the preset's when it is empty); after
// the handshake the state is released, data flows, a second HandshakeContext is
// a no-op.
func TestLxUTLSReleaseAfterHandshake(t *testing.T) {
	for _, c := range []struct {
		name       string
		id         utls.ClientHelloID
		nextProtos []string
		wire       []string
	}{
		{"chrome-h2", utls.HelloChrome_Auto, []string{"h2"}, []string{"h2"}},
		{"firefox-http1", utls.HelloFirefox_Auto, []string{"http/1.1"}, []string{"http/1.1"}},
		{"safari-preset", utls.HelloSafari_Auto, nil, []string{"h2", "http/1.1"}},
		{"randomized-h2", lxRandomizedFingerprint(randomizedFingerprint.Seed), []string{"h2"}, []string{"h2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, server := lxReleasePair(t)
			protos := make(chan []string, 1)
			go lxReleaseServer(t, server, 0, protos)
			conn := lxReleaseClient(client, c.id, c.nextProtos)
			defer conn.Close()

			require.NoError(t, conn.HandshakeContext(context.Background()))
			require.Equal(t, c.wire, <-protos)
			lxRequireReleased(t, conn.UConn)
			require.NoError(t, conn.HandshakeContext(context.Background()))
			lxEcho(t, conn)
		})
	}
}

// Two concurrent HandshakeContext calls: one handshake, one release, no panic (run with -race).
func TestLxUTLSReleaseConcurrentHandshake(t *testing.T) {
	client, server := lxReleasePair(t)
	go lxReleaseServer(t, server, 0, nil)
	conn := lxReleaseClient(client, utls.HelloChrome_Auto, []string{"h2"})
	defer conn.Close()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = conn.HandshakeContext(context.Background())
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	lxRequireReleased(t, conn.UConn)
	lxEcho(t, conn)
}

// A failed handshake keeps the state; a retry returns the same cached error.
func TestLxUTLSReleaseFailedHandshakeKeepsState(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		buf := make([]byte, 1)
		_, _ = server.Read(buf)
		server.Close()
	}()
	conn := lxReleaseClient(client, utls.HelloChrome_Auto, []string{"h2"})
	defer conn.Close()
	err := conn.HandshakeContext(context.Background())
	require.Error(t, err)
	require.True(t, conn.alpnApplied)
	require.NotNil(t, conn.HandshakeState.Hello)
	require.NotNil(t, conn.Extensions)
	require.Error(t, conn.HandshakeContext(context.Background()))
}

// TLS 1.2 keeps the state: browser presets enable renegotiation.
func TestLxUTLSReleaseKeepsTLS12(t *testing.T) {
	client, server := lxReleasePair(t)
	go lxReleaseServer(t, server, stdtls.VersionTLS12, nil)
	conn := lxReleaseClient(client, utls.HelloChrome_Auto, []string{"h2"})
	defer conn.Close()
	require.NoError(t, conn.HandshakeContext(context.Background()))
	require.Equal(t, uint16(utls.VersionTLS12), conn.ConnectionState().Version)
	require.NotNil(t, conn.HandshakeState.Hello)
	lxEcho(t, conn)
}

// REALITY end to end: a verified handshake through NewRealityServer (dest = a
// local crypto/tls server) releases the state; data flows.
func TestLxRealityReleaseAfterHandshake(t *testing.T) {
	target, err := stdtls.Listen("tcp", "127.0.0.1:0", &stdtls.Config{Certificates: []stdtls.Certificate{lxReleaseCert(t, "www.example.com")}})
	require.NoError(t, err)
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	targetAddr := target.Addr().(*net.TCPAddr)

	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	serverConfig, err := NewRealityServer(context.Background(), logger.NOP(), option.InboundTLSOptions{
		Enabled:    true,
		ServerName: "www.example.com",
		Reality: &option.InboundRealityOptions{
			Enabled: true,
			Handshake: option.InboundRealityHandshakeOptions{
				ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: uint16(targetAddr.Port)},
			},
			PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
			ShortID:    badoption.Listable[string]{"0123abcd"},
		},
	})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		tlsConn, err := ServerHandshake(context.Background(), conn, serverConfig)
		if err != nil {
			conn.Close()
			return
		}
		defer tlsConn.Close()
		_, _ = io.Copy(tlsConn, tlsConn)
	}()

	for _, fingerprint := range []string{"chrome"} {
		clientConfig, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: "www.example.com",
			UTLS:       &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint},
			Reality: &option.OutboundRealityOptions{
				Enabled:   true,
				PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
				ShortID:   "0123abcd",
			},
		})
		require.NoError(t, err)
		tcpConn, err := net.Dial("tcp", listener.Addr().String())
		require.NoError(t, err)
		conn, err := clientConfig.(*RealityClientConfig).ClientHandshake(context.Background(), tcpConn)
		require.NoError(t, err)
		wrapper := conn.(*realityClientConnWrapper)
		lxRequireReleased(t, wrapper.UConn)
		require.NoError(t, wrapper.HandshakeContext(context.Background()))
		lxEcho(t, conn)
		conn.Close()
	}

	// A wrong public key: the server takes the client for a browser and splices it
	// to dest; the handshake is not verified, the UConn goes to the fallback with
	// its state untouched (no release before the verified check).
	wrongKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	clientConfig, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "www.example.com",
		UTLS:       &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
		Reality: &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: base64.RawURLEncoding.EncodeToString(wrongKey.PublicKey().Bytes()),
			ShortID:   "0123abcd",
		},
	})
	require.NoError(t, err)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		tlsConn, err := ServerHandshake(context.Background(), conn, serverConfig)
		if err != nil {
			conn.Close()
			return
		}
		tlsConn.Close()
	}()
	tcpConn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	// "reality verification failed", or an EOF when the server closes the splice
	// first — either way an error and no panic.
	_, err = clientConfig.(*RealityClientConfig).ClientHandshake(context.Background(), tcpConn)
	require.Error(t, err)
}

// lx:end SPEC 122
