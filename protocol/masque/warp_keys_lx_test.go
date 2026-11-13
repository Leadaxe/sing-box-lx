package masque

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func warpTestKeys(t *testing.T) (privateKey string, publicKey string, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	privateDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(privateDER), base64.StdEncoding.EncodeToString(publicDER), key
}

// A WARP node written with nothing but what the registration returns: the
// profile supplies the certificate, the pin, the name, the authority and the
// keepalive.
func TestPrepareLXProfileCloudflare(t *testing.T) {
	t.Parallel()
	privateKey, publicKey, key := warpTestKeys(t)
	var options option.MASQUEClientEndpointOptions
	options.Profile = option.MASQUEProfileCloudflare
	options.PrivateKey = privateKey
	options.PublicKey = publicKey
	require.NoError(t, prepareLXProfile(&options))

	require.True(t, options.TLS.Enabled)
	require.Equal(t, "www.cloudflare.com", options.TLS.ServerName)
	require.Equal(t, badoption.Listable[string]{"cloudflareaccess.com"}, options.Headers["Host"])
	require.Equal(t, badoption.Duration(30*time.Second), options.HTTP2Options.KeepAlivePeriod)
	require.Equal(t, badoption.Duration(30*time.Second), options.HTTP3Options.HTTP2Options.KeepAlivePeriod)

	// The pair is one crypto/tls accepts, signed by the registration key and
	// not about to expire under a process that runs for months.
	pair, err := stdtls.X509KeyPair([]byte(strings.Join(options.TLS.ClientCertificate, "\n")), []byte(strings.Join(options.TLS.ClientKey, "\n")))
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.True(t, key.PublicKey.Equal(leaf.PublicKey))
	require.True(t, leaf.NotBefore.Before(time.Now()))
	require.True(t, leaf.NotAfter.After(time.Now().Add(5*365*24*time.Hour)))

	// The pin is what upstream's verifier computes from the peer certificate.
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	want := sha256.Sum256(publicDER)
	require.Equal(t, badoption.Listable[[]byte]{want[:]}, options.TLS.CertificatePublicKeySHA256)
}

// Explicit values win over the profile's defaults.
func TestPrepareLXProfileKeepsExplicit(t *testing.T) {
	t.Parallel()
	var options option.MASQUEClientEndpointOptions
	options.Profile = option.MASQUEProfileCloudflare
	options.TLS = &option.OutboundTLSOptions{ServerName: "example.org"}
	options.Headers = badoption.HTTPHeader{"host": {"other.example"}}
	options.HTTP2Options.KeepAlivePeriod = badoption.Duration(5 * time.Second)
	require.NoError(t, prepareLXProfile(&options))
	require.Equal(t, "example.org", options.TLS.ServerName)
	require.Len(t, options.Headers, 1)
	require.Equal(t, badoption.Duration(5*time.Second), options.HTTP2Options.KeepAlivePeriod)
	require.Empty(t, options.TLS.ClientCertificate)

	var noSNI option.MASQUEClientEndpointOptions
	noSNI.Profile = option.MASQUEProfileCloudflare
	noSNI.TLS = &option.OutboundTLSOptions{DisableSNI: true}
	require.NoError(t, prepareLXProfile(&noSNI))
	require.Empty(t, noSNI.TLS.ServerName)
}

func TestPrepareLXProfileRejects(t *testing.T) {
	t.Parallel()
	privateKey, publicKey, _ := warpTestKeys(t)

	var standard option.MASQUEClientEndpointOptions
	standard.PrivateKey = privateKey
	require.ErrorContains(t, prepareLXProfile(&standard), "need profile cloudflare")
	var untouched option.MASQUEClientEndpointOptions
	require.NoError(t, prepareLXProfile(&untouched))
	require.Nil(t, untouched.TLS)
	require.Empty(t, untouched.Headers)

	var both option.MASQUEClientEndpointOptions
	both.Profile = option.MASQUEProfileCloudflare
	both.PrivateKey = privateKey
	both.TLS = &option.OutboundTLSOptions{ClientKeyPath: "/k.pem"}
	require.ErrorContains(t, prepareLXProfile(&both), "private_key conflicts")

	var pinned option.MASQUEClientEndpointOptions
	pinned.Profile = option.MASQUEProfileCloudflare
	pinned.PublicKey = publicKey
	pinned.TLS = &option.OutboundTLSOptions{CertificatePublicKeySHA256: badoption.Listable[[]byte]{{1}}}
	require.ErrorContains(t, prepareLXProfile(&pinned), "public_key conflicts")

	var garbage option.MASQUEClientEndpointOptions
	garbage.Profile = option.MASQUEProfileCloudflare
	garbage.PrivateKey = "not base64!"
	require.ErrorContains(t, prepareLXProfile(&garbage), "private_key")
	garbage.PrivateKey = ""
	garbage.PublicKey = privateKey // a private key where the public one belongs
	require.ErrorContains(t, prepareLXProfile(&garbage), "public_key")
}

// lx: SPEC 060 — under a detour the first TLS record is fragmented by default,
// for either profile; an explicit choice in the config stands, and a direct
// dial is left alone.
func TestPrepareLXProfileDetourFragment(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{"", option.MASQUEProfileCloudflare} {
		var viaDetour option.MASQUEClientEndpointOptions
		viaDetour.Profile = profile
		viaDetour.Detour = "relay"
		viaDetour.TLS = &option.OutboundTLSOptions{Enabled: true}
		original := viaDetour.TLS
		require.NoError(t, prepareLXProfile(&viaDetour))
		require.True(t, viaDetour.TLS.RecordFragment, "profile %q", profile)
		require.False(t, original.RecordFragment, "the caller's TLS options must not be written through")

		var explicit option.MASQUEClientEndpointOptions
		explicit.Profile = profile
		explicit.Detour = "relay"
		explicit.TLS = &option.OutboundTLSOptions{Enabled: true, Fragment: true}
		require.NoError(t, prepareLXProfile(&explicit))
		require.False(t, explicit.TLS.RecordFragment, "profile %q", profile)

		var direct option.MASQUEClientEndpointOptions
		direct.Profile = profile
		direct.TLS = &option.OutboundTLSOptions{Enabled: true}
		require.NoError(t, prepareLXProfile(&direct))
		require.False(t, direct.TLS.RecordFragment, "profile %q", profile)
	}
}
