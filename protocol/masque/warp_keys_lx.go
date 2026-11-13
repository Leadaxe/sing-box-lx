package masque

// lx: SPEC 110 — what profile cloudflare fills in before the endpoint is
// built, so a WARP node is written with the values its registration returns
// and nothing else: the keys, the server, the addresses.

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
)

const (
	// warpDefaultServerName is sent when the config names none. The endpoint's
	// own name (consumer-masque.cloudflareclient.com) is what DPI matches on;
	// the endpoint serves the tunnel under any name and is authenticated by its
	// key, so a neutral Cloudflare property is the default (SPEC 021).
	warpDefaultServerName = "www.cloudflare.com"
	// warpAuthority is the authority of the tunnel request.
	warpAuthority = "cloudflareaccess.com"
	// warpKeepAlivePeriod is the fork's keepalive for WARP since 1.14; upstream's
	// default for the endpoint is 10 s.
	warpKeepAlivePeriod = 30 * time.Second
	// warpClientCertificateLifetime: the certificate is issued at every start
	// and WARP accepts a long-lived one (SPEC 110 §12, checked with 365 days).
	// The process may run for months, so it must not expire under it.
	warpClientCertificateLifetime = 10 * 365 * 24 * time.Hour
)

// prepareLXProfile applies the defaults and the key options of the profile to
// the endpoint options. Without profile cloudflare the fork's key options are
// an error, everything else is left to upstream.
func prepareLXProfile(options *option.MASQUEClientEndpointOptions) error {
	if options.Profile != option.MASQUEProfileCloudflare {
		if options.PrivateKey != "" || options.PublicKey != "" {
			return E.New("private_key and public_key need profile cloudflare; a standard server takes tls.client_certificate and tls.certificate_public_key_sha256")
		}
		if options.TLS != nil {
			tlsOptions := *options.TLS
			tls.ApplyDetourFragmentDefault(&tlsOptions, options.DialerOptions) // lx: SPEC 060
			options.TLS = &tlsOptions
		}
		return nil
	}
	tlsOptions := option.OutboundTLSOptions{}
	if options.TLS != nil {
		tlsOptions = *options.TLS
	}
	// lx: SPEC 060 — the HTTP/2 leg sends a ClientHello through the detour's
	// path like any TLS outbound; the fork fragments it there by default.
	tls.ApplyDetourFragmentDefault(&tlsOptions, options.DialerOptions)
	tlsOptions.Enabled = true
	if tlsOptions.ServerName == "" && !tlsOptions.DisableSNI {
		tlsOptions.ServerName = warpDefaultServerName
	}
	if options.PrivateKey != "" {
		if len(tlsOptions.ClientCertificate) > 0 || tlsOptions.ClientCertificatePath != "" || len(tlsOptions.ClientKey) > 0 || tlsOptions.ClientKeyPath != "" {
			return E.New("private_key conflicts with tls.client_certificate / tls.client_key: use one")
		}
		certificatePEM, keyPEM, err := issueWARPClientCertificate(options.PrivateKey)
		if err != nil {
			return E.Cause(err, "private_key")
		}
		tlsOptions.ClientCertificate = badoption.Listable[string]{certificatePEM}
		tlsOptions.ClientKey = badoption.Listable[string]{keyPEM}
	}
	if options.PublicKey != "" {
		if len(tlsOptions.CertificatePublicKeySHA256) > 0 {
			return E.New("public_key conflicts with tls.certificate_public_key_sha256: use one")
		}
		pin, err := warpPublicKeyPin(options.PublicKey)
		if err != nil {
			return E.Cause(err, "public_key")
		}
		tlsOptions.CertificatePublicKeySHA256 = badoption.Listable[[]byte]{pin}
	}
	options.TLS = &tlsOptions

	hasHost := false
	for name := range options.Headers {
		if strings.EqualFold(name, "Host") {
			hasHost = true
			break
		}
	}
	if !hasHost {
		headers := make(badoption.HTTPHeader, len(options.Headers)+1)
		for name, values := range options.Headers {
			headers[name] = values
		}
		headers["Host"] = badoption.Listable[string]{warpAuthority}
		options.Headers = headers
	}
	if options.HTTP2Options.KeepAlivePeriod == 0 {
		options.HTTP2Options.KeepAlivePeriod = badoption.Duration(warpKeepAlivePeriod)
	}
	if options.HTTP3Options.HTTP2Options.KeepAlivePeriod == 0 {
		options.HTTP3Options.HTTP2Options.KeepAlivePeriod = badoption.Duration(warpKeepAlivePeriod)
	}
	return nil
}

// issueWARPClientCertificate wraps the registration key into the self-signed
// certificate WARP's mTLS expects, as PEM.
func issueWARPClientCertificate(privateKeyBase64 string) (certificatePEM string, keyPEM string, err error) {
	keyDER, err := base64.StdEncoding.DecodeString(privateKeyBase64)
	if err != nil {
		return "", "", E.Cause(err, "decode base64")
	}
	privateKey, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return "", "", E.Cause(err, "parse EC private key")
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    now.Add(-24 * time.Hour),
		NotAfter:     now.Add(warpClientCertificateLifetime),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return "", "", E.Cause(err, "issue client certificate")
	}
	certificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certificatePEM, keyPEM, nil
}

// warpPublicKeyPin is the SHA-256 of the endpoint key's SubjectPublicKeyInfo,
// the form tls.certificate_public_key_sha256 takes.
func warpPublicKeyPin(publicKeyBase64 string) ([]byte, error) {
	keyDER, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil {
		return nil, E.Cause(err, "decode base64")
	}
	publicKey, err := x509.ParsePKIXPublicKey(keyDER)
	if err != nil {
		return nil, E.Cause(err, "parse PKIX public key")
	}
	if _, isECDSA := publicKey.(*ecdsa.PublicKey); !isECDSA {
		return nil, E.New("not an ECDSA public key")
	}
	canonicalDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	pin := sha256.Sum256(canonicalDER)
	return pin[:], nil
}
