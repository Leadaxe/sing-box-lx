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
	"strings"
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

	code, err := registry.mintCode("")
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
	code, _ := registry.mintCode("")
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

func TestGenerateCodeAlphabet(t *testing.T) {
	// Only the unambiguous alphabet (no 0/O/1/I) may ever appear, with dashes
	// at the fixed group boundaries. 100 runs to make a stray char surface.
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for range 100 {
		code, err := generateCode()
		if err != nil {
			t.Fatal(err)
		}
		for i, ch := range code {
			if i == 4 || i == 9 {
				if ch != '-' {
					t.Fatal("expected dash at group boundary:", code)
				}
				continue
			}
			if !strings.ContainsRune(alphabet, ch) {
				t.Fatal("character outside enrollment alphabet:", code)
			}
		}
	}
}

func TestEnrollNamePriority(t *testing.T) {
	registry := newTestRegistry(t)
	certDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}

	// The operator's label from mint wins over the enrollee's own name.
	code, err := registry.mintCode("operator-label")
	if err != nil {
		t.Fatal(err)
	}
	client, err := registry.enroll(code, "self-name", certDER)
	if err != nil {
		t.Fatal(err)
	}
	if client.Name != "operator-label" {
		t.Fatal("operator label must win, got:", client.Name)
	}

	// Without an operator label the enrollee's own name is kept.
	code, err = registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	client, err = registry.enroll(code, "self-name", certDER)
	if err != nil {
		t.Fatal(err)
	}
	if client.Name != "self-name" {
		t.Fatal("enrollee name must be kept without a label, got:", client.Name)
	}

	// Empty on both sides stays empty at the registry level; the "client"
	// default is the enrollment handler's job, not the registry's.
	code, err = registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	client, err = registry.enroll(code, "", certDER)
	if err != nil {
		t.Fatal(err)
	}
	if client.Name != "" {
		t.Fatal("registry must not invent a default name, got:", client.Name)
	}
}

func TestMintCodeReplacesPrevious(t *testing.T) {
	registry := newTestRegistry(t)
	certDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	// Only one code is live at a time: the replaced one must not enroll.
	if _, err = registry.enroll(first, "x", certDER); err == nil {
		t.Fatal("replaced code must be dead")
	}
	if _, err = registry.enroll(second, "x", certDER); err != nil {
		t.Fatal("latest code must enroll:", err)
	}
}

func TestDuplicateFingerprintListsTwiceRemovesBoth(t *testing.T) {
	registry := newTestRegistry(t)
	certDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	code, err := registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.enroll(code, "first", certDER); err != nil {
		t.Fatal(err)
	}
	code, err = registry.mintCode("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.enroll(code, "second", certDER); err != nil {
		t.Fatal(err)
	}

	// Current behavior: enrollment does not dedupe fingerprints, so the same
	// certificate enrolled through two codes yields two list entries pinning
	// one fingerprint.
	fingerprint := fingerprintOf(certDER)
	var matches int
	for _, client := range registry.list() {
		if client.Fingerprint == fingerprint {
			matches++
		}
	}
	if matches != 2 {
		t.Fatal("expected two entries with the same fingerprint, got:", matches)
	}

	// remove by fingerprint sweeps every entry pinning it — both go at once.
	removed, err := registry.remove(fingerprint)
	if err != nil || !removed {
		t.Fatal("remove by fingerprint failed:", err)
	}
	if registry.count() != 0 {
		t.Fatal("both duplicate entries must be gone, left:", registry.count())
	}
	if registry.isTrusted(fingerprint) {
		t.Fatal("fingerprint must no longer be trusted")
	}
}
