//go:build with_awg

package wireguard

import (
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// The full parameter set of a live AWG 3.1 export (amnezia-awg2 container,
// protocol_version 3.1) renders to the amneziawg-go v3 uapi keys: the
// base64 header key becomes hex, the ranged knobs keep their "min-max"
// spec, the two switches are emitted as 1.
func TestAwgIpcLinesAWG3(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{
		Jc: 4, Jmin: 10, Jmax: 50,
		S1: 55, S2: 42, S3: 40, S4: 12,
		H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey:    "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc=",
		ContentPaddingAddition: "10-100",
		RekeyAfterTime:         "100-120",
		RekeyTimeout:           "3-7",
		RejectAfterTime:        "150-180",
		KeepaliveTimeout:       "5-15",
		MaxHandshakeAttempts:   "15-20",
		RandomTrailers:         true,
		DisableCookies:         true,
	})
	require.NoError(t, err)
	require.Equal(t,
		"\njc=4\njmin=10\njmax=50\ns1=55\ns2=42\ns3=40\ns4=12"+
			"\nh1=1\nh2=2\nh3=3\nh4=4"+
			"\nheader_protection_key=a4135b437bc814c4d909b0a442d2ce6cd52db0cf61022f99865ae065b1f45e07"+
			"\ncontent_padding_addition=10-100"+
			"\nrekey_after_time=100-120\nrekey_timeout=3-7\nreject_after_time=150-180"+
			"\nkeepalive_timeout=5-15\nmax_handshake_attempts=15-20"+
			"\nrandom_trailers=1\ndisable_cookies=1",
		lines)
}

// Switches off and ranges unset are omitted: an AWG 2.0 config renders
// exactly as before AWG 3.x support.
func TestAwgIpcLinesAWG3UnsetOmitted(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{Jc: 4, Jmin: 40, Jmax: 70, S4: 12})
	require.NoError(t, err)
	require.Equal(t, "\njc=4\njmin=40\njmax=70\ns4=12", lines)
}

// A single-value timing ("rekey_timeout": 5) renders as a plain number.
func TestAwgIpcLinesAWG3SingleValueRange(t *testing.T) {
	t.Parallel()
	lines, err := awgIpcLines(option.AmneziaWGOptions{RekeyTimeout: "5", KeepaliveTimeout: "10-10"})
	require.NoError(t, err)
	require.Equal(t, "\nrekey_timeout=5\nkeepalive_timeout=10", lines)
}

// header_protection_key needs every S1–S4 to carry the 12-byte header cipher
// nonce; a short padding is a config error naming the field, caught at
// `sing-box check` rather than at IpcSet.
func TestAwgIpcLinesHeaderKeyRequiresPadding(t *testing.T) {
	t.Parallel()
	const key = "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc="
	for _, o := range []struct {
		options option.AmneziaWGOptions
		field   string
	}{
		{option.AmneziaWGOptions{HeaderProtectionKey: key, S1: 55, S2: 42, S3: 40, S4: 8}, "s4"},
		{option.AmneziaWGOptions{HeaderProtectionKey: key, S1: 11, S2: 42, S3: 40, S4: 12}, "s1"},
		{option.AmneziaWGOptions{HeaderProtectionKey: key}, "s1"},
	} {
		_, err := awgIpcLines(o.options)
		require.Error(t, err)
		require.ErrorContains(t, err, o.field)
		require.ErrorContains(t, err, "header_protection_key")
	}
	// exactly 12 everywhere is enough
	_, err := awgIpcLines(option.AmneziaWGOptions{HeaderProtectionKey: key, S1: 12, S2: 12, S3: 12, S4: 12})
	require.NoError(t, err)
}

func TestAwgIpcLinesHeaderKeyFormat(t *testing.T) {
	t.Parallel()
	pad := option.AmneziaWGOptions{S1: 12, S2: 12, S3: 12, S4: 12}
	bad := pad
	bad.HeaderProtectionKey = "not base64!"
	_, err := awgIpcLines(bad)
	require.Error(t, err)
	require.ErrorContains(t, err, "header_protection_key")

	short := pad
	short.HeaderProtectionKey = "AAAA"
	_, err = awgIpcLines(short)
	require.Error(t, err)
	require.ErrorContains(t, err, "32 bytes")

	zero := pad
	zero.HeaderProtectionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	_, err = awgIpcLines(zero)
	require.Error(t, err)
	require.ErrorContains(t, err, "all zeros")
}

// Invalid ranges are rejected with the key name (options built in code
// bypass JSON validation).
func TestAwgIpcLinesAWG3InvalidRange(t *testing.T) {
	t.Parallel()
	_, err := awgIpcLines(option.AmneziaWGOptions{RejectAfterTime: "180-150"})
	require.Error(t, err)
	require.ErrorContains(t, err, "reject_after_time")
	_, err = awgIpcLines(option.AmneziaWGOptions{ContentPaddingAddition: "ten"})
	require.Error(t, err)
	require.ErrorContains(t, err, "content_padding_addition")
}

// The per-peer keepalive renders "N", "min-max" or nothing.
func TestAwgKeepaliveSpec(t *testing.T) {
	t.Parallel()
	for spec, want := range map[option.AWGRange]string{"25": "25", "25-35": "25-35", "": "", "0": ""} {
		got, err := awgKeepaliveSpec(spec)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := awgKeepaliveSpec("35-25")
	require.Error(t, err)

	lines := peerConfig{publicKeyHex: "aa", keepalive: "25-35"}.GenerateIpcLines()
	require.True(t, strings.HasSuffix(lines, "\npersistent_keepalive_interval=25-35"), lines)
	lines = peerConfig{publicKeyHex: "aa", keepalive: ""}.GenerateIpcLines()
	require.NotContains(t, lines, "persistent_keepalive_interval")
}
