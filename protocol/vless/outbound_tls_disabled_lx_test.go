// lx:begin tls-disabled-dialer
package vless

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// Regression for SPEC 045: `"tls": {"enabled": false}` must not produce a TLS
// dialer wrapping a nil config — same defect as trojan, see
// protocol/trojan/outbound_tls_disabled_lx_test.go.
func TestNewOutboundTLSDisabledNoDialer(t *testing.T) {
	t.Parallel()
	adapter, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "test", option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     "127.0.0.1",
			ServerPort: 443,
		},
		UUID: "6e8a7f36-0c02-4f4a-9b39-0d73a37ba8a7",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: false},
		},
	})
	require.NoError(t, err)
	outbound := adapter.(*Outbound)
	require.Nil(t, outbound.tlsConfig)
	require.Nil(t, outbound.tlsDialer)
}

func TestNewOutboundTLSEnabledHasDialer(t *testing.T) {
	t.Parallel()
	adapter, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "test", option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     "127.0.0.1",
			ServerPort: 443,
		},
		UUID: "6e8a7f36-0c02-4f4a-9b39-0d73a37ba8a7",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true, Insecure: true},
		},
	})
	require.NoError(t, err)
	outbound := adapter.(*Outbound)
	require.NotNil(t, outbound.tlsConfig)
	require.NotNil(t, outbound.tlsDialer)
}

// lx:end tls-disabled-dialer
