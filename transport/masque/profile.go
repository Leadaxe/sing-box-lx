package masque

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"time"

	"github.com/sagernet/quic-go/http3"
	E "github.com/sagernet/sing/common/exceptions"
)

// Cloudflare WARP MASQUE defaults.
const (
	// CloudflareConnectSNI is the SNI Cloudflare's consumer MASQUE endpoint
	// expects. It intentionally does not match the endpoint IP/host — the
	// endpoint is authenticated by pinning its public key instead.
	CloudflareConnectSNI = "consumer-masque.cloudflareclient.com"
	// CloudflareConnectURI is the default CONNECT-IP request URI for WARP.
	CloudflareConnectURI = "https://cloudflareaccess.com"
)

// Profile selects the behavioural quirks of the CONNECT-IP dial. It varies four
// things; everything else (QUIC, http3, capsule/datagram, the userspace stack)
// is profile-independent. See SPEC 021.
type Profile struct {
	// RequestProtocol is the :protocol value of the Extended CONNECT request.
	// Cloudflare uses "cf-connect-ip"; RFC 9484 uses "connect-ip".
	RequestProtocol string
	// IgnoreExtendedConnect skips the "server enabled Extended CONNECT" check.
	// Cloudflare WARP does not send the setting, violating the RFC, so a strict
	// client would refuse; we tolerate it for the cloudflare profile.
	IgnoreExtendedConnect bool
	// H3ConnectProto, when non-empty, is sent as the HTTP/2 "cf-connect-proto"
	// header on the h2 path (Cloudflare-specific).
	H2ConnectProto string
	// DefaultSNI / DefaultURI are used when the config leaves sni/uri empty.
	DefaultSNI string
	DefaultURI string
	// PinPublicKey enables ECDSA public-key pinning against the endpoint's
	// certificate (Cloudflare Access model) instead of normal chain validation.
	PinPublicKey bool
}

// ProfileCloudflare matches Cloudflare WARP's non-RFC-compliant CONNECT-IP.
func ProfileCloudflare() Profile {
	return Profile{
		RequestProtocol:       "cf-connect-ip",
		IgnoreExtendedConnect: true,
		H2ConnectProto:        "cf-connect-ip",
		DefaultSNI:            CloudflareConnectSNI,
		DefaultURI:            CloudflareConnectURI,
		PinPublicKey:          true,
	}
}

// ProfileStandard is the strict RFC 9484 profile (for a self-hosted CONNECT-IP
// server). Kept minimal in v1 — see SPEC 021 phase 3.
func ProfileStandard() Profile {
	return Profile{
		RequestProtocol:       "connect-ip",
		IgnoreExtendedConnect: false,
		DefaultSNI:            "",
		DefaultURI:            "",
		PinPublicKey:          false,
	}
}

// ParseProfile resolves a profile name. Empty defaults to cloudflare.
func ParseProfile(name string) (Profile, error) {
	switch name {
	case "", "cloudflare":
		return ProfileCloudflare(), nil
	case "standard":
		return ProfileStandard(), nil
	default:
		return Profile{}, E.New("masque: unknown profile: ", name)
	}
}

// PrepareTLSConfig builds the client TLS config for a given profile.
//
// For the cloudflare profile it pins the endpoint's ECDSA public key: SNI does
// not match the endpoint, so normal verification is skipped (InsecureSkipVerify)
// and VerifyConnection asserts that a presented certificate carries exactly the
// expected public key. When insecure is true, pinning is disabled entirely.
func PrepareTLSConfig(profile Profile, privKey *ecdsa.PrivateKey, peerPubKey *ecdsa.PublicKey, sni string, insecure bool) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		ServerName: sni,
		NextProtos: []string{http3.NextProtoH3},
	}

	if privKey != nil {
		cert, err := generateClientCert(privKey)
		if err != nil {
			return nil, E.Cause(err, "generate client cert")
		}
		tlsConfig.Certificates = []tls.Certificate{{
			Certificate: cert,
			PrivateKey:  privKey,
		}}
	}

	if profile.PinPublicKey && !insecure {
		if peerPubKey == nil {
			return nil, errors.New("masque: public key required for pinning")
		}
		tlsConfig.InsecureSkipVerify = true
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			var joined error
			for _, cert := range cs.PeerCertificates {
				if err := verifyPinnedCert(cert, peerPubKey); err != nil {
					joined = errors.Join(joined, err)
					continue
				}
				return nil
			}
			if joined == nil {
				joined = errors.New("masque: no peer certificate to pin")
			}
			return joined
		}
	} else if insecure {
		tlsConfig.InsecureSkipVerify = true
	}

	return tlsConfig, nil
}

func verifyPinnedCert(cert *x509.Certificate, peerPubKey *ecdsa.PublicKey) error {
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		// We only support ECDSA endpoint keys (Cloudflare's cert type).
		return x509.ErrUnsupportedAlgorithm
	}
	if !pub.Equal(peerPubKey) {
		return errors.New("masque: remote endpoint has a different public key than pinned")
	}
	return nil
}

// generateClientCert creates a short-lived self-signed certificate wrapping the
// client's ECDSA key, presented to the endpoint (Cloudflare Access mTLS).
func generateClientCert(privKey *ecdsa.PrivateKey) ([][]byte, error) {
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    now,
		NotAfter:     now.Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, err
	}
	return [][]byte{der}, nil
}
