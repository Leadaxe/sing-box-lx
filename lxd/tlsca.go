//go:build with_lx_command

package lxd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// The daemon is its own tiny CA: it self-signs one long-lived identity for the
// server and one per enrolled client. Trust is by certificate pinning, not by
// a public CA — the launcher pins the server by the fingerprint carried in the
// invite string, the daemon pins each client by the fingerprint recorded at
// enrollment. Model mirrors WireGuard peers: private keys never leave their
// host, only public certs (fingerprints) are exchanged.

const certValidity = 10 * 365 * 24 * time.Hour

type identity struct {
	certPEM []byte
	keyPEM  []byte
	tlsCert tls.Certificate
	// fingerprint is the SHA-256 of the certificate DER, lowercase hex.
	fingerprint string
}

func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// generateIdentity self-signs an Ed25519-free ECDSA P-256 identity usable as
// both TLS server and client (mTLS needs client auth on the launcher side).
func generateIdentity(commonName string, timeNow time.Time) (*identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             timeNow.Add(-time.Hour),
		NotAfter:              timeNow.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &identity{certPEM: certPEM, keyPEM: keyPEM, tlsCert: tlsCert, fingerprint: fingerprintOf(der)}, nil
}

// loadOrCreateServerIdentity reads the server identity from the state dir, or
// creates and persists it on first boot. The fingerprint is stable across
// restarts — the property the invite string relies on — so a new identity is
// generated ONLY when the cert is genuinely absent: a transient IO failure
// must fail the boot, not silently mint a new fingerprint and void every pin
// the launchers hold.
func loadOrCreateServerIdentity(dir string, timeNow time.Time) (*identity, error) {
	certPath := filepath.Join(dir, "server_cert.pem")
	keyPath := filepath.Join(dir, "server_key.pem")
	certPEM, err := os.ReadFile(certPath)
	if err == nil {
		keyPEM, keyErr := os.ReadFile(keyPath)
		if keyErr != nil {
			return nil, E.Cause(keyErr, "read server key")
		}
		return identityFromPEM(certPEM, keyPEM)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, E.Cause(err, "read server cert")
	}
	if _, keyErr := os.Stat(keyPath); keyErr == nil {
		// A key without its cert is a half-broken identity; regenerating would
		// change the fingerprint behind the operator's back.
		return nil, E.New("server key exists but server cert is missing — restore ", certPath, " or delete ", keyPath, " to mint a new identity")
	}
	created, err := generateIdentity("sing-box-lxd", timeNow)
	if err != nil {
		return nil, E.Cause(err, "generate server identity")
	}
	if err = os.WriteFile(keyPath, created.keyPEM, 0o600); err != nil {
		return nil, E.Cause(err, "persist server key")
	}
	if err = os.WriteFile(certPath, created.certPEM, 0o644); err != nil {
		return nil, E.Cause(err, "persist server cert")
	}
	return created, nil
}

func identityFromPEM(certPEM, keyPEM []byte) (*identity, error) {
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, E.New("invalid certificate PEM")
	}
	return &identity{certPEM: certPEM, keyPEM: keyPEM, tlsCert: tlsCert, fingerprint: fingerprintOf(block.Bytes)}, nil
}
