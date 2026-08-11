//go:build with_lx_command

package lxd

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"sync"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// trustedClient is one enrolled launcher: its pinned certificate fingerprint
// plus human metadata for `client list`.
type trustedClient struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     string `json:"added_at"`
}

// clientRegistry holds the trusted clients and the single active one-time
// enrollment code. The code is single-use (it burns when one client registers)
// and has no TTL by design: it cannot be brute-forced, so it need not expire
// (owner decision 2026-08-08).
type clientRegistry struct {
	store  *store
	access sync.Mutex

	clients []trustedClient
	// activeCode is the single live enrollment code; activeCodeName is the
	// operator's label minted with it (wins over the enrollee's own name).
	activeCode     string
	activeCodeName string
}

const enrollCodeGroups = 3

func newClientRegistry(stateStore *store) (*clientRegistry, error) {
	registry := &clientRegistry{store: stateStore}
	loaded, err := stateStore.LoadClients()
	if err != nil {
		return nil, err
	}
	registry.clients = loaded
	return registry, nil
}

func (r *clientRegistry) count() int {
	r.access.Lock()
	defer r.access.Unlock()
	return len(r.clients)
}

func (r *clientRegistry) list() []trustedClient {
	r.access.Lock()
	defer r.access.Unlock()
	out := make([]trustedClient, len(r.clients))
	copy(out, r.clients)
	return out
}

// mintCode generates and stores a fresh one-time enrollment code, replacing
// any previous unused one (only one code is active at a time). name is the
// operator's label for the client that will redeem the code (may be empty).
func (r *clientRegistry) mintCode(name string) (string, error) {
	code, err := generateCode()
	if err != nil {
		return "", err
	}
	r.access.Lock()
	r.activeCode = code
	r.activeCodeName = name
	r.access.Unlock()
	return code, nil
}

// enroll validates the presented code (constant-time) and pins the client's
// certificate. The code burns on success, so it registers exactly one client.
func (r *clientRegistry) enroll(code, name string, clientCertDER []byte) (trustedClient, error) {
	r.access.Lock()
	defer r.access.Unlock()
	if r.activeCode == "" {
		return trustedClient{}, E.New("no active enrollment code")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(r.activeCode)) != 1 {
		return trustedClient{}, E.New("invalid enrollment code")
	}
	// The operator's label from `client add --name` wins over the name the
	// enrolling client suggests for itself.
	if r.activeCodeName != "" {
		name = r.activeCodeName
	}
	client := trustedClient{
		Name:        name,
		Fingerprint: fingerprintOf(clientCertDER),
		AddedAt:     nowStamp(),
	}
	next := append(append([]trustedClient(nil), r.clients...), client)
	if err := r.store.SaveClients(next); err != nil {
		return trustedClient{}, err
	}
	r.clients = next
	r.activeCode = "" // burn
	r.activeCodeName = ""
	return client, nil
}

func (r *clientRegistry) remove(nameOrFingerprint string) (bool, error) {
	r.access.Lock()
	defer r.access.Unlock()
	kept := make([]trustedClient, 0, len(r.clients))
	removed := false
	for _, client := range r.clients {
		if client.Name == nameOrFingerprint || client.Fingerprint == nameOrFingerprint {
			removed = true
			continue
		}
		kept = append(kept, client)
	}
	if !removed {
		return false, nil
	}
	if err := r.store.SaveClients(kept); err != nil {
		return false, err
	}
	r.clients = kept
	return true, nil
}

// isTrusted reports whether a leaf certificate fingerprint is pinned. It is
// enforced per request on the admin/gRPC planes (not at the TLS handshake):
// enrollment must run over a server-authenticated but not yet client-trusted
// connection, so the handshake merely requests the cert.
func (r *clientRegistry) isTrusted(fingerprint string) bool {
	r.access.Lock()
	defer r.access.Unlock()
	for _, client := range r.clients {
		if client.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}

// nowStamp is a fixed-format timestamp; wall clock is acceptable here because
// this code never runs inside a workflow script (the Date restriction is a
// workflow-only concern).
func nowStamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// generateCode makes a code like K7QM-XXNP-2RTD from an unambiguous alphabet
// (no 0/O/1/I). 5 bits per char, 20 per group, 60 total; brute force over the
// network is hopeless.
func generateCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	groups := make([]string, enrollCodeGroups)
	for g := range groups {
		var builder strings.Builder
		for range 4 {
			index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			builder.WriteByte(alphabet[index.Int64()])
		}
		groups[g] = builder.String()
	}
	return strings.Join(groups, "-"), nil
}

func decodeCertPEM(certPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, E.New("invalid client certificate PEM")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, E.Cause(err, "parse client certificate")
	}
	return block.Bytes, nil
}
