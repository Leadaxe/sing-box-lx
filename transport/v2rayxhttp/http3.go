//go:build with_xhttp && with_quic

package v2rayxhttp

import (
	"context"
	stdTLS "crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/tls"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 104 — XHTTP over HTTP/3, the transport Xray uses when tls.alpn is
// exactly ["h3"] (the server then listens on QUIC only).

const (
	// http3MaxIdleTimeout is Xray's net.ConnIdleTimeout. With ChromeParrot quic-go
	// pins its own value; it is set for parity with Xray's config.
	http3MaxIdleTimeout = 300 * time.Second
	// http3DefaultKeepAlivePeriod is Xray's QuicgoH3KeepAlivePeriod, used when
	// xmux.h_keep_alive_period is 0.
	http3DefaultKeepAlivePeriod = 10 * time.Second
)

// http3XmuxConn is one pooled HTTP/3 connection. http3.Transport keeps one QUIC
// connection per host and every request targets serverAddr, so the resource is
// one QUIC connection, as an http2.Transport is one TCP connection.
type http3XmuxConn struct {
	transport *http3.Transport
	// conn is the last QUIC connection the transport dialed; once it is gone the
	// resource is evicted instead of letting http3.Transport redial silently.
	conn   atomic.Pointer[quic.Conn]
	closed atomic.Bool
}

func (c *http3XmuxConn) Close() {
	if c.closed.Swap(true) {
		return
	}
	c.transport.Close()
}

func (c *http3XmuxConn) IsClosed() bool {
	if c.closed.Load() {
		return true
	}
	conn := c.conn.Load()
	if conn == nil {
		return false
	}
	select {
	case <-conn.Context().Done():
		return true
	default:
		return false
	}
}

func (c *http3XmuxConn) roundTripper() http.RoundTripper {
	return c.transport
}

// http3KeepAlivePeriod maps xmux.h_keep_alive_period to quic.Config: 0 is
// Xray's 10 s default, a negative value turns keep-alive off.
func http3KeepAlivePeriod(period time.Duration) time.Duration {
	switch {
	case period == 0:
		return http3DefaultKeepAlivePeriod
	case period < 0:
		return 0
	default:
		return period
	}
}

// http3PathMTUDiscovery reports whether quic-go path MTU discovery stays on:
// Xray turns it off everywhere except linux, windows and darwin (Android
// included).
func http3PathMTUDiscovery() bool {
	switch runtime.GOOS {
	case "linux", "windows", "darwin":
		return true
	default:
		return false
	}
}

// newHTTP3Transport returns the factory of pooled HTTP/3 connections. warn
// receives the load-time warnings of SPEC 104 §6.
func newHTTP3Transport(dialer N.Dialer, serverAddr M.Socksaddr, tlsConfig tls.Config, keepAlivePeriod time.Duration, warn func(string)) (func() xmuxConn, error) {
	stdConfig, fromUTLS, err := tls.STDConfigForQUIC(tlsConfig)
	var dialErr error
	if err != nil {
		if !errors.Is(err, tls.ErrECHOverQUIC) {
			return nil, E.Cause(err, "v2ray-xhttp: HTTP/3 TLS config")
		}
		// One bad subscription node must not fail the whole config: it loads, its
		// dials fail.
		warn("ECH is not supported over HTTP/3")
		dialErr = E.New("v2ray-xhttp: ECH is not supported over HTTP/3")
		stdConfig = &stdTLS.Config{}
	}
	if fromUTLS {
		warn("utls fingerprint is not applied over HTTP/3, the QUIC handshake uses the Chrome profile")
	}
	chromeParrot := true
	if stdConfig.VerifyConnection != nil {
		// quic-go refuses VerifyConnection with ChromeParrot; certificate
		// verification outranks the look of the handshake.
		chromeParrot = false
		warn("disable_sni turns off the Chrome QUIC profile for this server")
	}
	if len(stdConfig.Certificates) > 0 {
		// ChromeParrot refuses Certificates (a server-side field for quic-go); a
		// client certificate travels through GetClientCertificate instead.
		certificate := stdConfig.Certificates[0]
		stdConfig.Certificates = nil
		stdConfig.GetClientCertificate = func(*stdTLS.CertificateRequestInfo) (*stdTLS.Certificate, error) {
			return &certificate, nil
		}
	}
	stdConfig.NextProtos = []string{http3.NextProtoH3}
	quicConfig := &quic.Config{
		MaxIdleTimeout:          http3MaxIdleTimeout,
		KeepAlivePeriod:         http3KeepAlivePeriod(keepAlivePeriod),
		MaxIncomingStreams:      -1,
		DisablePathMTUDiscovery: !http3PathMTUDiscovery(),
		ChromeParrot:            chromeParrot,
	}
	return func() xmuxConn {
		conn := &http3XmuxConn{}
		conn.transport = &http3.Transport{
			TLSClientConfig: stdConfig,
			QUICConfig:      quicConfig,
			// The address argument is the URL host, which is serverAddr anyway; the
			// TLS config is ours, not the transport's copy, which would put the URL
			// host into an SNI that disable_sni left empty.
			Dial: func(ctx context.Context, _ string, _ *stdTLS.Config, quicConfig *quic.Config) (*quic.Conn, error) {
				if dialErr != nil {
					return nil, dialErr
				}
				packetConn, err := dialer.DialContext(ctx, N.NetworkUDP, serverAddr)
				if err != nil {
					return nil, E.Cause(err, "v2ray-xhttp: HTTP/3 needs UDP to the server")
				}
				quicConn, err := quic.DialEarlyConn(ctx, packetConn, stdConfig.Clone(), quicConfig)
				if err != nil {
					packetConn.Close()
					return nil, err
				}
				// quic-go does not own the conn it was given: close it when the QUIC
				// connection ends.
				go func() {
					<-quicConn.Context().Done()
					packetConn.Close()
				}()
				conn.conn.Store(quicConn)
				return quicConn, nil
			},
		}
		return conn
	}, nil
}

// quicHiddenError replaces a quic-go / http3 error at the conn boundary. It keeps
// the text and drops Timeout()/Temporary(): the conn returns its saved read
// error on every Read, and a consumer that takes Timeout() for an expired
// deadline (or retries Temporary()) and reads again would spin without a
// syscall (the SPEC 082 class). Connection-level errors keep unwrapping to
// net.ErrClosed so the relay still sees a closed connection.
type quicHiddenError struct {
	text   string
	closed bool
}

func (e *quicHiddenError) Error() string {
	return e.text
}

func (e *quicHiddenError) Unwrap() error {
	if e.closed {
		return net.ErrClosed
	}
	return nil
}

func hideQUICError(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	var (
		idleTimeoutError        *quic.IdleTimeoutError
		handshakeTimeoutError   *quic.HandshakeTimeoutError
		statelessResetError     *quic.StatelessResetError
		transportError          *quic.TransportError
		applicationError        *quic.ApplicationError
		versionNegotiationError *quic.VersionNegotiationError
		streamError             *quic.StreamError
		h3Error                 *http3.Error
	)
	switch {
	case errors.As(err, &idleTimeoutError),
		errors.As(err, &handshakeTimeoutError),
		errors.As(err, &statelessResetError),
		errors.As(err, &transportError),
		errors.As(err, &applicationError),
		errors.As(err, &versionNegotiationError):
		return &quicHiddenError{text: err.Error(), closed: true}
	case errors.As(err, &streamError), errors.As(err, &h3Error):
		return &quicHiddenError{text: err.Error()}
	default:
		return err
	}
}
