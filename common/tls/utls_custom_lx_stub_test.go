//go:build with_utls && !with_lx_utls_custom

package tls

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/stretchr/testify/require"
)

func TestCustomClientHelloRequiresBuildTag(t *testing.T) {
	var opts option.OutboundTLSOptions
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"server_name":"example.com","utls":{"enabled":true,"fingerprint":"custom","client_hello":{"cipher_suites":["TLS_AES_128_GCM_SHA256"],"compression_methods":["NULL"],"extensions":[{"name":"server_name"}]}}}`), &opts))
	_, err := NewUTLSClient(context.Background(), nil, "example.com", opts)
	require.ErrorContains(t, err, "with_lx_utls_custom")
}
