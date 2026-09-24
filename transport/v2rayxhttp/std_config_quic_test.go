//go:build with_xhttp && with_quic && with_utls

package v2rayxhttp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func quicTLSOptions(hash []byte) option.OutboundTLSOptions {
	return option.OutboundTLSOptions{
		Enabled:                    true,
		ServerName:                 "localhost",
		ALPN:                       []string{"h3"},
		CertificatePublicKeySHA256: [][]byte{hash},
		MinVersion:                 "1.2",
		CipherSuites:               []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
	}
}

func newTestTLSClient(t *testing.T, options option.OutboundTLSOptions) tls.Config {
	t.Helper()
	config, err := tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "localhost", options)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// TestSTDConfigForQUICFromUTLS is the guard of SPEC 104 §9: the uTLS → crypto/tls
// conversion must verify certificates exactly like the std client built from the
// same options. A field newUTLSClient starts filling that the conversion misses
// shows up here.
func TestSTDConfigForQUICFromUTLS(t *testing.T) {
	own, ownHash := testCertificate(t)
	other, _ := testCertificate(t)

	utlsOptions := quicTLSOptions(ownHash)
	utlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}
	converted, fromUTLS, err := tls.STDConfigForQUIC(newTestTLSClient(t, utlsOptions))
	if err != nil {
		t.Fatal(err)
	}
	if !fromUTLS {
		t.Fatal("fromUTLS = false for a uTLS config")
	}
	reference, fromUTLS, err := tls.STDConfigForQUIC(newTestTLSClient(t, quicTLSOptions(ownHash)))
	if err != nil || fromUTLS {
		t.Fatalf("std config: fromUTLS=%v err=%v", fromUTLS, err)
	}

	if converted.ServerName != "localhost" || converted.ServerName != reference.ServerName {
		t.Fatalf("ServerName = %q, std %q", converted.ServerName, reference.ServerName)
	}
	if strings.Join(converted.NextProtos, ",") != "h3" {
		t.Fatalf("NextProtos = %v", converted.NextProtos)
	}
	if converted.InsecureSkipVerify != reference.InsecureSkipVerify ||
		converted.MinVersion != reference.MinVersion ||
		converted.MaxVersion != reference.MaxVersion ||
		len(converted.CipherSuites) != len(reference.CipherSuites) ||
		converted.RootCAs != reference.RootCAs ||
		(converted.VerifyConnection == nil) != (reference.VerifyConnection == nil) ||
		converted.VerifyPeerCertificate == nil || reference.VerifyPeerCertificate == nil {
		t.Fatalf("converted config differs from std:\n%+v\n%+v", converted, reference)
	}
	for name, config := range map[string]*tls.STDConfig{"converted": converted, "std": reference} {
		if err := config.VerifyPeerCertificate(own.Certificate, nil); err != nil {
			t.Fatalf("%s rejects the pinned key: %v", name, err)
		}
		if err := config.VerifyPeerCertificate(other.Certificate, nil); err == nil {
			t.Fatalf("%s accepts a foreign key", name)
		}
	}
}

func TestSTDConfigForQUICDisableSNI(t *testing.T) {
	options := option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "localhost",
		ALPN:       []string{"h3"},
		DisableSNI: true,
		UTLS:       &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
	}
	converted, _, err := tls.STDConfigForQUIC(newTestTLSClient(t, options))
	if err != nil {
		t.Fatal(err)
	}
	if converted.ServerName != "" || converted.VerifyConnection == nil || !converted.InsecureSkipVerify {
		t.Fatalf("disable_sni: ServerName=%q VerifyConnection=%v InsecureSkipVerify=%v", converted.ServerName, converted.VerifyConnection != nil, converted.InsecureSkipVerify)
	}
	// The pool then runs this server without the Chrome profile, with a warning.
	var warnings []string
	newConn, err := newHTTP3Transport(N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), newTestTLSClient(t, options), 0, func(message string) { warnings = append(warnings, message) })
	if err != nil {
		t.Fatal(err)
	}
	conn := newConn().(*http3XmuxConn)
	defer conn.Close()
	if conn.transport.QUICConfig.ChromeParrot {
		t.Fatal("ChromeParrot left on with VerifyConnection")
	}
	if strings.Join(warnings, "\n") != "utls fingerprint is not applied over HTTP/3, the QUIC handshake uses the Chrome profile\ndisable_sni turns off the Chrome QUIC profile for this server" {
		t.Fatalf("warnings = %q", warnings)
	}
}

// TestHTTP3ECHLoadsDialFails: an ECH node loads, warns, and its dials fail.
func TestHTTP3ECHLoadsDialFails(t *testing.T) {
	for _, useUTLS := range []bool{false, true} {
		options := option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: "localhost",
			ALPN:       []string{"h3"},
			ECH:        &option.OutboundECHOptions{Enabled: true},
		}
		if useUTLS {
			options.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}
		}
		config := newTestTLSClient(t, options)
		if _, _, err := tls.STDConfigForQUIC(config); !errors.Is(err, tls.ErrECHOverQUIC) {
			t.Fatalf("utls=%v: STDConfigForQUIC err = %v", useUTLS, err)
		}
		transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:9"), option.V2RayXHTTPOptions{Mode: modeStreamOne}, config)
		if err != nil {
			t.Fatalf("utls=%v: ECH node failed to load: %v", useUTLS, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = transport.DialContext(ctx)
		cancel()
		transport.Close()
		if err == nil || !strings.Contains(err.Error(), "ECH is not supported over HTTP/3") {
			t.Fatalf("utls=%v: dial err = %v", useUTLS, err)
		}
	}
}

// TestHTTP3UTLSLoads: the reporter's shape (issue #25) — uTLS chrome, pinned
// key, alpn h3 — loads on HTTP/3 instead of failing.
func TestHTTP3UTLSLoads(t *testing.T) {
	_, hash := testCertificate(t)
	options := quicTLSOptions(hash)
	options.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}
	transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), option.V2RayXHTTPOptions{Mode: modeStreamUp}, newTestTLSClient(t, options))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if version := transport.(*Client).httpVersion; version != httpVersion3 {
		t.Fatalf("version = %s", version)
	}
}
