package vless

import (
	"encoding/base64"
	"strings"
	"testing"
)

// key32 and key1184 are the two accepted public-key sizes, base64url-encoded the
// way a real spec string carries them.
func key32() string   { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }
func key1184() string { return base64.RawURLEncoding.EncodeToString(make([]byte, 1184)) }

func TestParseClientEncryptionAccepts(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		xorMode uint32
		seconds uint32
		keys    int
		padding string
	}{
		{
			name:    "native 0rtt, x25519 key",
			spec:    "mlkem768x25519plus.native.0rtt." + key32(),
			xorMode: xorModeNative, seconds: 1, keys: 1,
		},
		{
			name:    "xorpub 1rtt, mlkem key",
			spec:    "mlkem768x25519plus.xorpub.1rtt." + key1184(),
			xorMode: xorModeXorPub, seconds: 0, keys: 1,
		},
		{
			name:    "random appearance",
			spec:    "mlkem768x25519plus.random.0rtt." + key32(),
			xorMode: xorModeRandom, seconds: 1, keys: 1,
		},
		{
			name:    "both key sizes together",
			spec:    "mlkem768x25519plus.native.0rtt." + key32() + "." + key1184(),
			xorMode: xorModeNative, seconds: 1, keys: 2,
		},
		{
			// Padding blocks precede the keys and are only meaningful in 1-RTT.
			name:    "padding blocks then key",
			spec:    "mlkem768x25519plus.native.1rtt.100-111-1111.75-0-111." + key32(),
			xorMode: xorModeNative, seconds: 0, keys: 1,
			padding: "100-111-1111.75-0-111",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseClientEncryption(tc.spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.xorMode != tc.xorMode {
				t.Errorf("xorMode = %d, want %d", cfg.xorMode, tc.xorMode)
			}
			if cfg.seconds != tc.seconds {
				t.Errorf("seconds = %d, want %d", cfg.seconds, tc.seconds)
			}
			if len(cfg.keys) != tc.keys {
				t.Errorf("keys = %d, want %d", len(cfg.keys), tc.keys)
			}
			if cfg.padding != tc.padding {
				t.Errorf("padding = %q, want %q", cfg.padding, tc.padding)
			}
		})
	}
}

// A bad spec must fail at config time with a message naming the offending part —
// the alternative is a connection that silently never establishes.
func TestParseClientEncryptionRejects(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		wantSub string
	}{
		{"empty", "", "empty encryption string"},
		{"too few segments", "mlkem768x25519plus.native.0rtt", "at least"},
		{"unknown method", "x25519only.native.0rtt." + key32(), "unsupported encryption method"},
		{"unknown appearance", "mlkem768x25519plus.plaid.0rtt." + key32(), "unknown encryption appearance"},
		{"unknown rtt", "mlkem768x25519plus.native.7rtt." + key32(), "unknown encryption RTT mode"},
		{"empty segment", "mlkem768x25519plus.native.0rtt." + key32() + ".", "empty segment"},
		{
			// Long enough to be read as a key, but not valid base64url.
			"key not base64", "mlkem768x25519plus.native.0rtt." + strings.Repeat("!", 44),
			"not base64url",
		},
		{
			// Decodes cleanly but is neither 32 nor 1184 bytes.
			"wrong key length", "mlkem768x25519plus.native.0rtt." + base64.RawURLEncoding.EncodeToString(make([]byte, 64)),
			"invalid encryption key length",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClientEncryption(tc.spec)
			if err == nil {
				t.Fatalf("expected an error for %q", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestParseClientEncryptionFieldShape mirrors the shape the subscription that
// motivated SPEC 032 actually ships: native/0rtt with a single ML-KEM-768 key,
// which base64url-encodes to a 1579-character segment. The key itself is
// synthetic — only its size matters to the parser, and a real one is a server
// credential that does not belong in the tree.
func TestParseClientEncryptionFieldShape(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, keyLenMLKEM768))
	if len(key) != 1579 {
		t.Fatalf("encoded key is %d chars, expected the 1579 seen in the field", len(key))
	}
	cfg, err := parseClientEncryption("mlkem768x25519plus.native.0rtt." + key)
	if err != nil {
		t.Fatalf("field-shaped spec rejected: %v", err)
	}
	if cfg.xorMode != xorModeNative || cfg.seconds != 1 {
		t.Fatalf("xorMode=%d seconds=%d, want native/0rtt", cfg.xorMode, cfg.seconds)
	}
	if len(cfg.keys) != 1 || len(cfg.keys[0]) != keyLenMLKEM768 {
		t.Fatalf("expected one %d-byte key, got %d", keyLenMLKEM768, len(cfg.keys))
	}
}
