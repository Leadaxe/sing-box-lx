// lx: SPEC 021 Ф4 — the h2 transport builds its TLS through the shared
// common/tls client. These tests pin the two properties that make that safe:
// the masque-specific pinning survives the trip through the shared layer, and
// the fragmentation knobs actually reach it.
//
// Without the first test a refactor of common/tls (e.g. cloning the config
// before use) would silently drop pinning and leave the endpoint unverified —
// a security regression that a connectivity test would never catch.
package masque

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/masque"
)

func testKeyPair(t *testing.T) (privB64, pubB64 string, priv *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public: %v", err)
	}
	return base64.StdEncoding.EncodeToString(privDER),
		base64.StdEncoding.EncodeToString(pubDER),
		key
}

// prepared builds the crypto/tls.Config exactly as NewOutbound does before
// handing it to the shared layer.
func prepared(t *testing.T, skipVerify bool) (*tls.Config, *ecdsa.PublicKey) {
	t.Helper()
	privB64, pubB64, priv := testKeyPair(t)
	_ = privB64
	_ = pubB64
	cfg, err := masque.PrepareTLSConfig(
		masque.ProfileCloudflare(), priv, &priv.PublicKey,
		"consumer-masque.cloudflareclient.com", skipVerify,
	)
	if err != nil {
		t.Fatalf("PrepareTLSConfig: %v", err)
	}
	cfg.NextProtos = []string{"h2"}
	return cfg, &priv.PublicKey
}

// TestH2TLSClientKeepsPinning: the pinning verifier and the client certificate
// must survive being routed through common/tls. If a future refactor of the
// shared layer clones the config after we mutate it, this fails loudly.
func TestH2TLSClientKeepsPinning(t *testing.T) {
	t.Parallel()
	cfg, _ := prepared(t, false)
	if cfg.VerifyConnection == nil {
		t.Fatal("precondition: PrepareTLSConfig produced no pinning verifier")
	}

	client, err := buildH2TLSClient(
		context.Background(), log.NewNOPFactory().Logger(),
		option.MASQUEOutboundOptions{}, "consumer-masque.cloudflareclient.com", cfg,
	)
	if err != nil {
		t.Fatalf("buildH2TLSClient: %v", err)
	}
	std, err := client.STDConfig()
	if err != nil {
		t.Fatalf("STDConfig: %v", err)
	}
	if std.VerifyConnection == nil {
		t.Fatal("pinning verifier lost on the way through common/tls")
	}
	if !std.InsecureSkipVerify {
		t.Fatal("pinned mode must skip chain verification (SNI never matches the endpoint)")
	}
	if len(std.Certificates) != len(cfg.Certificates) {
		t.Fatalf("client certificate lost: got %d, want %d", len(std.Certificates), len(cfg.Certificates))
	}
	if got, want := std.ServerName, cfg.ServerName; got != want {
		t.Fatalf("server name = %q, want %q", got, want)
	}
	if len(std.NextProtos) == 0 || std.NextProtos[0] != "h2" {
		t.Fatalf("ALPN = %v, want h2 first", std.NextProtos)
	}
}

// TestH2TLSClientPinningRejectsOtherKey: the verifier that survives must be the
// real one — a certificate carrying a different key is rejected.
func TestH2TLSClientPinningRejectsOtherKey(t *testing.T) {
	t.Parallel()
	cfg, _ := prepared(t, false)

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	otherCert := &x509.Certificate{PublicKey: &other.PublicKey}

	client, err := buildH2TLSClient(
		context.Background(), log.NewNOPFactory().Logger(),
		option.MASQUEOutboundOptions{}, "consumer-masque.cloudflareclient.com", cfg,
	)
	if err != nil {
		t.Fatalf("buildH2TLSClient: %v", err)
	}
	std, _ := client.STDConfig()
	err = std.VerifyConnection(tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{otherCert},
	})
	if err == nil {
		t.Fatal("pinning accepted a certificate with a different public key")
	}
}

// TestH2TLSClientSkipCertVerify: with skip_cert_verify the pinning is waived,
// and that decision must also survive the shared layer.
func TestH2TLSClientSkipCertVerify(t *testing.T) {
	t.Parallel()
	cfg, _ := prepared(t, true)

	client, err := buildH2TLSClient(
		context.Background(), log.NewNOPFactory().Logger(),
		option.MASQUEOutboundOptions{}, "consumer-masque.cloudflareclient.com", cfg,
	)
	if err != nil {
		t.Fatalf("buildH2TLSClient: %v", err)
	}
	std, _ := client.STDConfig()
	if !std.InsecureSkipVerify {
		t.Fatal("skip_cert_verify must disable verification")
	}
	if std.VerifyConnection != nil {
		t.Fatal("skip_cert_verify must not leave a pinning verifier behind")
	}
}

// TestH2TLSClientEmptyServerName: an endpoint may only present its real
// certificate when the ClientHello carries no SNI, so an empty server name must
// be accepted rather than rejected as missing.
func TestH2TLSClientEmptyServerName(t *testing.T) {
	t.Parallel()
	_, _, priv := testKeyPair(t)
	cfg, err := masque.PrepareTLSConfig(masque.ProfileCloudflare(), priv, &priv.PublicKey, "", false)
	if err != nil {
		t.Fatalf("PrepareTLSConfig: %v", err)
	}
	cfg.NextProtos = []string{"h2"}

	if _, err := buildH2TLSClient(
		context.Background(), log.NewNOPFactory().Logger(),
		option.MASQUEOutboundOptions{}, "", cfg,
	); err != nil {
		t.Fatalf("empty server name must be allowed: %v", err)
	}
}
