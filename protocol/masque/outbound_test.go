package masque

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

func TestParsePrefixes(t *testing.T) {
	t.Parallel()
	// bare v4 → /32, bare v6 → /128
	prefixes, err := parsePrefixes("172.16.0.2", "2606:4700::1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 2 {
		t.Fatalf("expected 2 prefixes, got %d", len(prefixes))
	}
	if prefixes[0].String() != "172.16.0.2/32" {
		t.Fatalf("v4 = %s", prefixes[0])
	}
	if prefixes[1].String() != "2606:4700::1/128" {
		t.Fatalf("v6 = %s", prefixes[1])
	}

	// explicit mask preserved
	prefixes, err = parsePrefixes("10.0.0.0/24", "")
	if err != nil {
		t.Fatal(err)
	}
	if prefixes[0].String() != "10.0.0.0/24" {
		t.Fatalf("masked v4 = %s", prefixes[0])
	}

	// none → error
	if _, err = parsePrefixes("", ""); err == nil {
		t.Fatal("expected error when no address given")
	}

	// bad → error
	if _, err = parsePrefixes("not-an-ip", ""); err == nil {
		t.Fatal("expected error for malformed ip")
	}
}

func TestParseECKeys(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	gotPriv, err := parseECPrivateKey(base64.StdEncoding.EncodeToString(privDER))
	if err != nil {
		t.Fatalf("parse private: %v", err)
	}
	if !gotPriv.Equal(priv) {
		t.Fatal("round-tripped private key mismatch")
	}

	gotPub, err := parseECPublicKey(base64.StdEncoding.EncodeToString(pubDER))
	if err != nil {
		t.Fatalf("parse public: %v", err)
	}
	if !gotPub.Equal(&priv.PublicKey) {
		t.Fatal("round-tripped public key mismatch")
	}

	// garbage base64 → error
	if _, err = parseECPrivateKey("!!!!"); err == nil {
		t.Fatal("expected error for bad base64")
	}
	// valid base64 but not an EC key → error
	if _, err = parseECPublicKey(base64.StdEncoding.EncodeToString([]byte("nope"))); err == nil {
		t.Fatal("expected error for non-key DER")
	}
}
