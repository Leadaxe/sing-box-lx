package masque

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"

	"github.com/sagernet/quic-go/http3"
)

func TestParseProfile(t *testing.T) {
	t.Parallel()
	cf, err := ParseProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if cf.RequestProtocol != "cf-connect-ip" || !cf.IgnoreExtendedConnect || !cf.PinPublicKey {
		t.Fatalf("empty should default to cloudflare, got %+v", cf)
	}
	if cf.DefaultSNI != CloudflareConnectSNI || cf.DefaultURI != CloudflareConnectURI {
		t.Fatalf("cloudflare defaults wrong: %+v", cf)
	}

	std, err := ParseProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	if std.RequestProtocol != "connect-ip" || std.IgnoreExtendedConnect || std.PinPublicKey {
		t.Fatalf("standard profile wrong: %+v", std)
	}

	if _, err = ParseProfile("bogus"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestPrepareTLSConfigPinning(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := PrepareTLSConfig(ProfileCloudflare(), priv, &peer.PublicKey, "example.org", false)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("cloudflare pinning must set InsecureSkipVerify")
	}
	if cfg.VerifyConnection == nil {
		t.Fatal("cloudflare pinning must set VerifyConnection")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatal("client cert must be generated when private key provided")
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != http3.NextProtoH3 {
		t.Fatalf("ALPN should be h3, got %v", cfg.NextProtos)
	}

	// Standard profile: no pinning, normal verification.
	stdCfg, err := PrepareTLSConfig(ProfileStandard(), nil, nil, "example.org", false)
	if err != nil {
		t.Fatal(err)
	}
	if stdCfg.InsecureSkipVerify || stdCfg.VerifyConnection != nil {
		t.Fatal("standard profile must not pin")
	}

	// Insecure disables pinning even for cloudflare.
	insecureCfg, err := PrepareTLSConfig(ProfileCloudflare(), priv, &peer.PublicKey, "example.org", true)
	if err != nil {
		t.Fatal(err)
	}
	if insecureCfg.VerifyConnection != nil {
		t.Fatal("insecure must disable VerifyConnection")
	}
}

func TestVerifyPinnedCert(t *testing.T) {
	t.Parallel()
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	cert := &x509.Certificate{PublicKey: &priv.PublicKey}
	if err := verifyPinnedCert(cert, &priv.PublicKey); err != nil {
		t.Fatalf("matching key should verify: %v", err)
	}
	if err := verifyPinnedCert(cert, &other.PublicKey); err == nil {
		t.Fatal("mismatched key must fail")
	}

	rsaLike := &x509.Certificate{PublicKey: "not-a-key"}
	if err := verifyPinnedCert(rsaLike, &priv.PublicKey); err == nil {
		t.Fatal("non-ECDSA key must be rejected")
	}
}
