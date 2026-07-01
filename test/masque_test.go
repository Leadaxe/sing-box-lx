package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func masqueTestKeys(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	privDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(privDER), base64.StdEncoding.EncodeToString(pubDER)
}

// TestMASQUEOptionDecode verifies the option type is registered and a valid
// cloudflare config decodes through the outbound registry.
func TestMASQUEOptionDecode(t *testing.T) {
	priv, pub := masqueTestKeys(t)
	config := `{
		"outbounds": [
			{
				"type": "masque",
				"tag": "warp",
				"server": "162.159.198.1",
				"server_port": 443,
				"profile": "cloudflare",
				"network": "h3",
				"private_key": "` + priv + `",
				"public_key": "` + pub + `",
				"ip": "172.16.0.2/32",
				"ipv6": "2606:4700:110::/128"
			}
		]
	}`

	ctx := include.Context(context.Background())
	var options option.Options
	err := json.UnmarshalContext(ctx, []byte(config), &options)
	require.NoError(t, err)
	require.Len(t, options.Outbounds, 1)
	require.Equal(t, "masque", options.Outbounds[0].Type)

	masqueOpts, ok := options.Outbounds[0].Options.(*option.MASQUEOutboundOptions)
	require.True(t, ok, "outbound options must decode to *MASQUEOutboundOptions")
	require.Equal(t, "cloudflare", masqueOpts.Profile)
	require.Equal(t, "h3", masqueOpts.Network)
	require.Equal(t, priv, masqueOpts.PrivateKey)
	require.Equal(t, "172.16.0.2/32", masqueOpts.IP)
}
