//go:build with_lx_command

package lxd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testClientCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newTestRegistry(t *testing.T) *clientRegistry {
	t.Helper()
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newClientRegistry(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestEnrollBurnsCodeAndPins(t *testing.T) {
	registry := newTestRegistry(t)
	certPEM := testClientCertPEM(t)
	certDER, err := decodeCertPEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}

	// No code minted yet.
	if _, err = registry.enroll("ANY", "x", certDER); err == nil {
		t.Fatal("enroll must fail without an active code")
	}

	code, err := registry.mintCode()
	if err != nil {
		t.Fatal(err)
	}

	// Wrong code rejected.
	if _, err = registry.enroll("WRONG-CODE", "x", certDER); err == nil {
		t.Fatal("wrong code must be rejected")
	}

	client, err := registry.enroll(code, "laptop", certDER)
	if err != nil {
		t.Fatal("valid enroll failed:", err)
	}
	if client.Fingerprint != fingerprintOf(certDER) {
		t.Fatal("fingerprint mismatch")
	}
	if !registry.isTrusted(client.Fingerprint) {
		t.Fatal("enrolled client must be trusted")
	}
	if registry.count() != 1 {
		t.Fatal("expected exactly one client")
	}

	// Code is single-use: it burned.
	if _, err = registry.enroll(code, "again", certDER); err == nil {
		t.Fatal("code must be single-use")
	}
}

func TestClientRegistryPersistsAndRemoves(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newClientRegistry(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	certDER, _ := decodeCertPEM(testClientCertPEM(t))
	code, _ := registry.mintCode()
	client, err := registry.enroll(code, "laptop", certDER)
	if err != nil {
		t.Fatal(err)
	}

	// Reload from disk: trust must survive.
	reloaded, err := newClientRegistry(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.isTrusted(client.Fingerprint) {
		t.Fatal("trust must persist across reload")
	}

	removed, err := reloaded.remove("laptop")
	if err != nil || !removed {
		t.Fatal("remove by name failed")
	}
	if reloaded.isTrusted(client.Fingerprint) {
		t.Fatal("removed client must not be trusted")
	}
	removedAgain, _ := reloaded.remove("laptop")
	if removedAgain {
		t.Fatal("removing an absent client must report false")
	}
}

func TestGenerateCodeShape(t *testing.T) {
	code, err := generateCode()
	if err != nil {
		t.Fatal(err)
	}
	// 3 groups of 4, dash-separated: XXXX-XXXX-XXXX = 14 chars.
	if len(code) != 14 || code[4] != '-' || code[9] != '-' {
		t.Fatal("unexpected code shape:", code)
	}
}
