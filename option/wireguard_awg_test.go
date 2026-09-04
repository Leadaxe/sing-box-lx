package option_test

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestMagicHeaderUnmarshal(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		input    string
		expected option.MagicHeader
	}{
		{"number", `1234567890`, "1234567890"},
		{"number max uint32", `4294967295`, "4294967295"},
		{"number zero is unset", `0`, ""},
		{"string number", `"1234567890"`, "1234567890"},
		{"string range", `"43613244-384550127"`, "43613244-384550127"},
		{"string range full uint32", `"0-4294967295"`, "0-4294967295"},
		{"string degenerate range", `"7-7"`, "7"},
		{"string zero is unset", `"0"`, ""},
		{"string empty is unset", `""`, ""},
		{"string with spaces", `" 10 - 20 "`, "10-20"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var header option.MagicHeader
			require.NoError(t, json.Unmarshal([]byte(testCase.input), &header))
			require.Equal(t, testCase.expected, header)
		})
	}
}

func TestMagicHeaderUnmarshalError(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		input string
	}{
		{"start greater than end", `"100-50"`},
		{"number above uint32", `4294967296`},
		{"range part above uint32", `"1-4294967296"`},
		{"negative number", `-1`},
		{"float number", `1.5`},
		{"garbage string", `"junk"`},
		{"trailing garbage", `"123-456-789"`},
		{"missing range start", `"-5"`},
		{"missing range end", `"5-"`},
		{"bool", `true`},
		{"object", `{}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var header option.MagicHeader
			require.Error(t, json.Unmarshal([]byte(testCase.input), &header))
		})
	}
}

// The config decoder (sing contextjson) prefixes errors with the JSON path,
// so a bad value reports the exact field: `h1: invalid range ...` (the
// message is shared with every AWG "number or range" field since AWG 3.x).
func TestMagicHeaderErrorNamesField(t *testing.T) {
	t.Parallel()
	var options option.AmneziaWGOptions
	err := json.Unmarshal([]byte(`{"h1": "100-50"}`), &options)
	require.Error(t, err)
	require.ErrorContains(t, err, "h1")
	require.ErrorContains(t, err, "invalid range")
}

func TestMagicHeaderMarshal(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		header   option.MagicHeader
		expected string
	}{
		{"single value as number", "1234567890", `1234567890`},
		{"range as string", "43613244-384550127", `"43613244-384550127"`},
		{"unset as zero number", "", `0`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			content, err := json.Marshal(testCase.header)
			require.NoError(t, err)
			require.Equal(t, testCase.expected, string(content))
		})
	}
}

// Existing configs hold plain numbers in h1..h4; they must read identically
// and re-marshal back to numbers (type fidelity with the former uint32 field).
func TestAmneziaWGOptionsRoundTrip(t *testing.T) {
	t.Parallel()
	legacy := `{"jc":4,"jmin":40,"jmax":70,"h1":1,"h2":2,"h3":3,"h4":4,"i1":"<b 0xf6>"}`
	var options option.AmneziaWGOptions
	require.NoError(t, json.Unmarshal([]byte(legacy), &options))
	require.Equal(t, option.MagicHeader("1"), options.H1)
	require.Equal(t, option.MagicHeader("4"), options.H4)
	content, err := json.Marshal(options)
	require.NoError(t, err)
	require.Contains(t, string(content), `"h1":1`)
	require.NotContains(t, string(content), `"h1":"1"`)

	ranged := `{"h1":"43613244-384550127","h2":"826869626-2105069164"}`
	options = option.AmneziaWGOptions{}
	require.NoError(t, json.Unmarshal([]byte(ranged), &options))
	content, err = json.Marshal(options)
	require.NoError(t, err)
	require.Contains(t, string(content), `"h1":"43613244-384550127"`)
}

func TestAmneziaWGOptionsIsSet(t *testing.T) {
	t.Parallel()
	require.False(t, option.AmneziaWGOptions{}.IsSet())
	require.True(t, option.AmneziaWGOptions{H1: "10-20"}.IsSet())
	require.True(t, option.AmneziaWGOptions{Jc: 4}.IsSet())

	var options option.AmneziaWGOptions
	require.NoError(t, json.Unmarshal([]byte(`{"h1":0,"h2":"0","h3":""}`), &options))
	require.False(t, options.IsSet())
}

func TestMagicHeaderSpec(t *testing.T) {
	t.Parallel()
	spec, err := option.MagicHeader("10-20").Spec()
	require.NoError(t, err)
	require.Equal(t, "10-20", spec)

	spec, err = option.MagicHeader("").Spec()
	require.NoError(t, err)
	require.Equal(t, "", spec)

	// Programmatically constructed (bypassing JSON) garbage must still fail.
	_, err = option.MagicHeader("junk").Spec()
	require.Error(t, err)
	_, err = option.MagicHeader("100-50").Spec()
	require.Error(t, err)
}
