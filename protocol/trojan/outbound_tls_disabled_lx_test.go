// lx:begin tls-disabled-dialer
package trojan

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// Regression for SPEC 045: `"tls": {"enabled": false}` must not produce a TLS
// dialer wrapping a nil config — that config crashed the whole process with a
// nil pointer dereference on the first handshake (upstream regression from the
// ECH retry commit, which replaced the `tlsConfig != nil` dial-time check with
// `tlsDialer != nil`).
func TestNewOutboundTLSDisabledNoDialer(t *testing.T) {
	t.Parallel()
	adapter, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "test", option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     "127.0.0.1",
			ServerPort: 41393,
		},
		Password: "password",
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
	adapter, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "test", option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     "127.0.0.1",
			ServerPort: 443,
		},
		Password: "password",
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
