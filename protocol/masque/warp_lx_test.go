package masque

import (
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	transportHTTP "github.com/sagernet/sing-box/transport/http"
	"github.com/sagernet/sing-box/transport/masque"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func TestApplyLXProfile(t *testing.T) {
	t.Parallel()
	address := badoption.Listable[netip.Prefix]{
		netip.MustParsePrefix("172.16.0.2/32"),
		netip.MustParsePrefix("2606:4700:110:8a36::1/128"),
	}
	cloudflareHTTP := transportHTTP.TunnelLXOptions{
		PlainConnectHTTP2: true,
		PlainConnectHeaders: http.Header{
			"Cf-Connect-Proto": []string{"cf-connect-ip"},
			"Pq-Enabled":       []string{"false"},
		},
		HTTP3ConnectBudget:      3 * time.Second,
		HTTP3AdditionalSettings: map[uint64]uint64{0x276: 1},
	}
	for _, testCase := range []struct {
		name         string
		profile      string
		address      []netip.Prefix
		expectedHTTP transportHTTP.TunnelLXOptions
		expectedLX   masque.ClientLXOptions
	}{
		{name: "empty"},
		{name: "standard", profile: option.MASQUEProfileStandard},
		{
			name:       "standard with address",
			profile:    option.MASQUEProfileStandard,
			address:    address,
			expectedLX: masque.ClientLXOptions{Address: address},
		},
		{
			name:         "cloudflare",
			profile:      option.MASQUEProfileCloudflare,
			expectedHTTP: cloudflareHTTP,
			expectedLX:   masque.ClientLXOptions{Protocol: "cf-connect-ip", RawStreamCapsules: true},
		},
		{
			name:         "cloudflare with address",
			profile:      option.MASQUEProfileCloudflare,
			address:      address,
			expectedHTTP: cloudflareHTTP,
			expectedLX:   masque.ClientLXOptions{Protocol: "cf-connect-ip", Address: address, RawStreamCapsules: true},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var options option.MASQUEClientEndpointOptions
			options.Profile = testCase.profile
			options.Address = testCase.address
			httpOptions, clientOptions, err := applyLXProfile(options, transportHTTP.ClientOptions{Version: 3}, masque.ClientOptions{Path: "/"})
			require.NoError(t, err)
			require.Equal(t, testCase.expectedHTTP, httpOptions.TunnelLX)
			require.Equal(t, testCase.expectedLX, clientOptions.LX)
			// Options outside the fork's part pass through untouched.
			require.Equal(t, 3, httpOptions.Version)
			require.Equal(t, "/", clientOptions.Path)
		})
	}
}

func TestApplyLXProfileUnknown(t *testing.T) {
	t.Parallel()
	var options option.MASQUEClientEndpointOptions
	options.Profile = "warp"
	_, _, err := applyLXProfile(options, transportHTTP.ClientOptions{}, masque.ClientOptions{})
	require.ErrorContains(t, err, "unknown masque profile: warp")
}
