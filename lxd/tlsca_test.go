//go:build with_lxd

package lxd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerIdentityStableAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateServerIdentity(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certBefore, err := os.ReadFile(filepath.Join(dir, "server_cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(filepath.Join(dir, "server_key.pem"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := loadOrCreateServerIdentity(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The invite string pins this fingerprint: a restart must never mint a new one.
	if second.fingerprint != first.fingerprint {
		t.Fatal("fingerprint changed across loads:", first.fingerprint, "->", second.fingerprint)
	}
	certAfter, _ := os.ReadFile(filepath.Join(dir, "server_cert.pem"))
	keyAfter, _ := os.ReadFile(filepath.Join(dir, "server_key.pem"))
	if !bytes.Equal(certBefore, certAfter) {
		t.Fatal("server_cert.pem rewritten on second load")
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("server_key.pem rewritten on second load")
	}
}

func TestServerIdentityCorruptCertFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateServerIdentity(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "server_cert.pem")
	garbage := []byte("not a certificate at all")
	if err := os.WriteFile(certPath, garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadOrCreateServerIdentity(dir, time.Now()); err == nil {
		t.Fatal("corrupt cert must fail the load, not be silently accepted")
	}
	// The broken file must survive for the operator to inspect: silently
	// regenerating would void every pin the launchers hold.
	after, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, garbage) {
		t.Fatal("corrupt cert was rewritten on load failure")
	}
}

func TestServerIdentityUnreadableCertFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 0 does not block reads for root")
	}
	dir := t.TempDir()
	original, err := loadOrCreateServerIdentity(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "server_cert.pem")
	if err = os.Chmod(certPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(certPath, 0o644) })

	// A transient IO failure is not "cert absent": it must fail the boot
	// instead of minting a fresh identity behind the operator's back.
	if _, err = loadOrCreateServerIdentity(dir, time.Now()); err == nil {
		t.Fatal("unreadable cert must fail the load, not trigger regeneration")
	}

	if err = os.Chmod(certPath, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadOrCreateServerIdentity(dir, time.Now())
	if err != nil {
		t.Fatal("load after restoring permissions failed:", err)
	}
	if recovered.fingerprint != original.fingerprint {
		t.Fatal("fingerprint changed after transient IO failure")
	}
}

func TestServerIdentityKeyWithoutCertFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateServerIdentity(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "server_key.pem")
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "server_cert.pem")); err != nil {
		t.Fatal(err)
	}

	_, err = loadOrCreateServerIdentity(dir, time.Now())
	if err == nil {
		t.Fatal("orphaned key must fail the load, not be silently replaced")
	}
	if !strings.Contains(err.Error(), "server key exists") {
		t.Fatal("error must explain the orphaned key, got:", err)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("orphaned key was rewritten on load failure")
	}
}

func TestGenerateIdentityCertProperties(t *testing.T) {
	now := time.Now()
	created, err := generateIdentity("test-identity", now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(created.certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("certPEM is not a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("certificate does not parse:", err)
	}

	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("expected ECDSA public key, got %T", cert.PublicKey)
	}
	if pub.Curve != elliptic.P256() {
		t.Fatal("expected P-256 curve, got:", pub.Curve.Params().Name)
	}

	// Fingerprint contract: SHA-256 of the DER, lowercase hex. Recompute it
	// here from primitives so a drift in fingerprintOf is caught too.
	sum := sha256.Sum256(block.Bytes)
	if created.fingerprint != hex.EncodeToString(sum[:]) {
		t.Fatal("fingerprint is not sha256(DER) lowercase hex:", created.fingerprint)
	}

	// Long-lived identity: roughly ten years (plus the 1h clock-skew backdate).
	lifetime := cert.NotAfter.Sub(cert.NotBefore)
	if lifetime < certValidity || lifetime > certValidity+2*time.Hour {
		t.Fatal("unexpected certificate lifetime:", lifetime)
	}

	var hasServerAuth, hasClientAuth bool
	for _, usage := range cert.ExtKeyUsage {
		switch usage {
		case x509.ExtKeyUsageServerAuth:
			hasServerAuth = true
		case x509.ExtKeyUsageClientAuth:
			hasClientAuth = true
		}
	}
	if !hasServerAuth || !hasClientAuth {
		t.Fatal("identity must be usable for both server and client auth (mTLS)")
	}
}

func TestServerIdentityKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateServerIdentity(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "server_key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("server key must be 0600, got %o", perm)
	}
}
