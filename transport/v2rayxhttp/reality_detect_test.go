package v2rayxhttp

import "testing"

// The Reality detector matches by runtime type NAME (not by importing the
// with_utls-only concrete types), so these stand-ins reproduce exactly what the
// detector sees: types named RealityClientConfig / KTLSClientConfig, and a kTLS
// wrapper that embeds an inner Config — mirroring common/tls. If common/tls ever
// renames RealityClientConfig, this test stays green but the live path breaks;
// that is the documented fragility (SPECS/TASKS/011 PLAN §3.4-A) — keep the names in sync.

// RealityClientConfig is a stand-in whose Name() equals the real one.
type RealityClientConfig struct{}

// UTLSClientConfig stands in for a non-Reality uTLS config.
type UTLSClientConfig struct{}

// KTLSClientConfig stands in for the kTLS wrapper: it embeds an inner config via
// a field named "Config", exactly like common/tls.KTLSClientConfig.
type KTLSClientConfig struct {
	Config any
}

func TestTypeIsReality(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"nil", nil, false},
		{"reality", &RealityClientConfig{}, true},
		{"utls", &UTLSClientConfig{}, false},
		{"ktls-wrapping-reality", &KTLSClientConfig{Config: &RealityClientConfig{}}, true},
		{"ktls-wrapping-utls", &KTLSClientConfig{Config: &UTLSClientConfig{}}, false},
		{"ktls-wrapping-nil", &KTLSClientConfig{Config: nil}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typeIsReality(tc.in, 0); got != tc.want {
				t.Fatalf("typeIsReality(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
