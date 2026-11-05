package option_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestMASQUEClientLXOptions(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		input           string
		expectedProfile string
		expectedAddress []netip.Prefix
	}{
		{
			name:  "none",
			input: `{"server":"engage.cloudflareclient.com","server_port":443}`,
		},
		{
			name:            "single address",
			input:           `{"server":"engage.cloudflareclient.com","server_port":443,"profile":"cloudflare","address":"172.16.0.2/32"}`,
			expectedProfile: option.MASQUEProfileCloudflare,
			expectedAddress: []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")},
		},
		{
			name:            "address list",
			input:           `{"server":"engage.cloudflareclient.com","server_port":443,"version":2,"profile":"standard","address":["172.16.0.2/32","2606:4700:110:8a36::1/128"]}`,
			expectedProfile: option.MASQUEProfileStandard,
			expectedAddress: []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32"), netip.MustParsePrefix("2606:4700:110:8a36::1/128")},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var options option.MASQUEClientEndpointOptions
			require.NoError(t, json.UnmarshalContext(ctx, []byte(testCase.input), &options))
			require.Equal(t, testCase.expectedProfile, options.Profile)
			require.Equal(t, testCase.expectedAddress, []netip.Prefix(options.Address))

			content, err := json.Marshal(options)
			require.NoError(t, err)
			var decoded option.MASQUEClientEndpointOptions
			require.NoError(t, json.UnmarshalContext(ctx, content, &decoded))
			require.Equal(t, options.Profile, decoded.Profile)
			require.Equal(t, options.Address, decoded.Address)
			require.Equal(t, options.ServerOptions, decoded.ServerOptions)
			require.Equal(t, options.Version, decoded.Version)
		})
	}
}

func TestMASQUEClientLXOptionsMarshalOmitsEmpty(t *testing.T) {
	t.Parallel()
	var options option.MASQUEClientEndpointOptions
	content, err := json.Marshal(options)
	require.NoError(t, err)
	require.NotContains(t, string(content), `"profile"`)
	require.NotContains(t, string(content), `"address"`)
}

func TestMASQUEClientLXOptionsUnknownKey(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"", `,"version":1`, `,"version":2`, `,"version":3`} {
		var options option.MASQUEClientEndpointOptions
		input := `{"server":"example.com","profile":"cloudflare","addresses":["172.16.0.2/32"]` + version + `}`
		require.Error(t, json.UnmarshalContext(context.Background(), []byte(input), &options), input)
	}
}
