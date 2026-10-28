//go:build with_utls && with_lx_command

package tls

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 — the `reality` verdict stage (SPEC §4, criterion 5).

func lxRealityVerdicts(t *testing.T, summary lxtrace.Summary) []lxtrace.StageSnapshot {
	t.Helper()
	var verdicts []lxtrace.StageSnapshot
	for _, stage := range summary.Stages {
		if stage.Kind == "reality" {
			verdicts = append(verdicts, stage)
		}
	}
	return verdicts
}

func lxVerdictOf(stage lxtrace.StageSnapshot) string {
	for _, attr := range stage.Attrs {
		if attr.Key == "verdict" {
			return attr.Value
		}
	}
	return ""
}

func TestLxRealityVerdictClassification(t *testing.T) {
	config := lxNewRealityClient(t, "chrome", "", false, false)
	for _, tc := range []struct {
		name     string
		verified bool
		err      error
		verdict  string
		result   lxtrace.Result
	}{
		{"accepted", true, nil, "accepted", lxtrace.OK},
		{"dest_cert", false, nil, "dest_cert", lxtrace.Failed},
		{"invalid_cert", false, x509.UnknownAuthorityError{}, "invalid_cert", lxtrace.Failed},
		{"expired", false, x509.CertificateInvalidError{Reason: x509.Expired}, "invalid_cert", lxtrace.Failed},
		{"reset", false, errors.New("connection reset by peer"), "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := lxtrace.New(lxtrace.Options{})
			realityVerdictLx(lxtrace.With(context.Background(), trace), config, tc.verified, tc.err)
			verdicts := lxRealityVerdicts(t, trace.Finish(nil))
			if tc.verdict == "" {
				require.Empty(t, verdicts, "a handshake error without a certificate gives no verdict")
				return
			}
			require.Len(t, verdicts, 1)
			require.Equal(t, tc.verdict, lxVerdictOf(verdicts[0]))
			require.Equal(t, tc.result, verdicts[0].Result)
		})
	}
	// Without a trace: nothing, and the config is not even asked for its name.
	realityVerdictLx(context.Background(), &RealityClientConfig{}, true, nil)
}

// End to end through ClientHandshake: the right key is accepted, a wrong key
// is not recognized. The dest here has a self-signed certificate, so the
// splice to dest ends in invalid_cert; a dest with a publicly trusted
// certificate gives dest_cert (the classification test covers it).
func TestLxRealityVerdictEndToEnd(t *testing.T) {
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
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	serverConfig, err := NewRealityServer(context.Background(), logger.NOP(), option.InboundTLSOptions{
		Enabled:    true,
		ServerName: "www.example.com",
		Reality: &option.InboundRealityOptions{
			Enabled: true,
			Handshake: option.InboundRealityHandshakeOptions{
				ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: uint16(target.Addr().(*net.TCPAddr).Port)},
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
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				tlsConn, err := ServerHandshake(context.Background(), conn, serverConfig)
				if err != nil {
					conn.Close()
					return
				}
				defer tlsConn.Close()
				_, _ = io.Copy(tlsConn, tlsConn)
			}()
		}
	}()

	handshake := func(publicKey []byte) (lxtrace.Summary, error) {
		clientConfig, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: "www.example.com",
			UTLS:       &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			Reality: &option.OutboundRealityOptions{
				Enabled:   true,
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
				ShortID:   "0123abcd",
			},
		})
		require.NoError(t, err)
		tcpConn, err := net.Dial("tcp", listener.Addr().String())
		require.NoError(t, err)
		defer tcpConn.Close()
		trace := lxtrace.New(lxtrace.Options{})
		conn, err := ClientHandshake(lxtrace.With(context.Background(), trace), tcpConn, clientConfig)
		if conn != nil {
			conn.Close()
		}
		return trace.Finish(nil), err
	}

	summary, err := handshake(privateKey.PublicKey().Bytes())
	require.NoError(t, err)
	verdicts := lxRealityVerdicts(t, summary)
	require.Len(t, verdicts, 1)
	require.Equal(t, "accepted", lxVerdictOf(verdicts[0]))
	require.Equal(t, lxtrace.OK, verdicts[0].Result)
	require.Equal(t, "tls", summary.Stages[0].Kind)
	require.Equal(t, summary.Stages[0].ID, verdicts[0].Parent, "the verdict is a child of the tls stage")

	wrongKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	summary, err = handshake(wrongKey.PublicKey().Bytes())
	require.Error(t, err)
	verdicts = lxRealityVerdicts(t, summary)
	require.Len(t, verdicts, 1)
	require.Equal(t, "invalid_cert", lxVerdictOf(verdicts[0]))
	require.Equal(t, verdicts[0].ID, summary.RootCause, "the verdict is the root cause of the failed handshake")
}
