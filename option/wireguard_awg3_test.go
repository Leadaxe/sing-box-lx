package option_test

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

// AWG 3.x fields: the ranged knobs accept a number or "min-max", the key is a
// string, the switches are booleans — all promoted to the endpoint root.
func TestAWG3OptionsUnmarshal(t *testing.T) {
	t.Parallel()
	var options option.WireGuardEndpointOptions
	err := json.Unmarshal([]byte(`{
		"private_key": "S+OfsezHHNoj0an9U5uZhUE2X30RbM5KeI8quL2BrXI=",
		"address": ["10.8.1.7/32"],
		"jc": 4, "jmin": 10, "jmax": 50,
		"s1": 55, "s2": 42, "s3": 40, "s4": 12,
		"h1": 1, "h2": 2, "h3": 3, "h4": 4,
		"header_protection_key": "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc=",
		"content_padding_addition": "10-100",
		"rekey_after_time": "100-120",
		"rekey_timeout": "3-7",
		"reject_after_time": "150-180",
		"keepalive_timeout": "5-15",
		"max_handshake_attempts": "15-20",
		"random_trailers": true,
		"disable_cookies": true,
		"peers": [{
			"address": "77.239.123.44", "port": 30565,
			"public_key": "Zc3Wyk7OEsDsw5ouoROUSazkW1aIyXv6KI3tSkSm5FU=",
			"allowed_ips": ["0.0.0.0/0"],
			"persistent_keepalive_interval": "25-35"
		}]
	}`), &options)
	require.NoError(t, err)
	require.Equal(t, "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc=", options.HeaderProtectionKey)
	require.Equal(t, option.AWGRange("10-100"), options.ContentPaddingAddition)
	require.Equal(t, option.AWGRange("100-120"), options.RekeyAfterTime)
	require.Equal(t, option.AWGRange("3-7"), options.RekeyTimeout)
	require.Equal(t, option.AWGRange("150-180"), options.RejectAfterTime)
	require.Equal(t, option.AWGRange("5-15"), options.KeepaliveTimeout)
	require.Equal(t, option.AWGRange("15-20"), options.MaxHandshakeAttempts)
	require.True(t, options.RandomTrailers)
	require.True(t, options.DisableCookies)
	require.True(t, options.AmneziaWGOptions.IsSet())
	require.Len(t, options.Peers, 1)
	require.Equal(t, option.AWGRange("25-35"), options.Peers[0].PersistentKeepaliveInterval)
	require.True(t, options.Peers[0].PersistentKeepaliveInterval.IsRange())
}

// The per-peer keepalive keeps accepting the plain WireGuard number (the only
// form every existing config uses) and marshals back to a number.
func TestPeerKeepaliveNumberCompat(t *testing.T) {
	t.Parallel()
	var peer option.WireGuardPeer
	require.NoError(t, json.Unmarshal([]byte(`{"public_key": "x", "persistent_keepalive_interval": 25}`), &peer))
	require.Equal(t, option.AWGRange("25"), peer.PersistentKeepaliveInterval)
	require.False(t, peer.PersistentKeepaliveInterval.IsRange())
	spec, err := peer.PersistentKeepaliveInterval.Spec()
	require.NoError(t, err)
	require.Equal(t, "25", spec)

	out, err := json.Marshal(peer)
	require.NoError(t, err)
	require.Contains(t, string(out), `"persistent_keepalive_interval":25`)

	// 0 / omitted = off, stays omitted on the way out
	require.NoError(t, json.Unmarshal([]byte(`{"public_key": "x", "persistent_keepalive_interval": 0}`), &peer))
	require.Equal(t, option.AWGRange(""), peer.PersistentKeepaliveInterval)
	out, err = json.Marshal(peer)
	require.NoError(t, err)
	require.NotContains(t, string(out), "persistent_keepalive_interval")

	// inverted range is a config error naming the field
	err = json.Unmarshal([]byte(`{"public_key": "x", "persistent_keepalive_interval": "35-25"}`), &peer)
	require.Error(t, err)
	require.ErrorContains(t, err, "persistent_keepalive_interval")
}

// A single-value range marshals as a number, a span as a string — for every
// AWG 3.x ranged field, so a round-tripped config keeps its shape.
func TestAWG3RangeMarshalFidelity(t *testing.T) {
	t.Parallel()
	options := option.AmneziaWGOptions{
		ContentPaddingAddition: "10-100",
		RekeyTimeout:           "5",
		RandomTrailers:         true,
	}
	out, err := json.Marshal(options)
	require.NoError(t, err)
	require.Contains(t, string(out), `"content_padding_addition":"10-100"`)
	require.Contains(t, string(out), `"rekey_timeout":5`)
	require.Contains(t, string(out), `"random_trailers":true`)
	require.NotContains(t, string(out), "reject_after_time")
	require.NotContains(t, string(out), "disable_cookies")
}

// The AWG 3.x fields are AWG fields: any of them alone makes IsSet true, so
// a build without with_awg rejects them instead of silently dropping them.
func TestAWG3FieldsCountAsSet(t *testing.T) {
	t.Parallel()
	require.True(t, option.AmneziaWGOptions{HeaderProtectionKey: "k"}.IsSet())
	require.True(t, option.AmneziaWGOptions{ContentPaddingAddition: "10-100"}.IsSet())
	require.True(t, option.AmneziaWGOptions{RekeyTimeout: "3-7"}.IsSet())
	require.True(t, option.AmneziaWGOptions{RandomTrailers: true}.IsSet())
	require.True(t, option.AmneziaWGOptions{DisableCookies: true}.IsSet())
	require.False(t, option.AmneziaWGOptions{}.IsSet())
}
