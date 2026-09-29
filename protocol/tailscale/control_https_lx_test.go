//go:build with_gvisor

package tailscale

import (
	"testing"

	"github.com/sagernet/tailscale/envknob"
)

func TestForceNoise443Default(t *testing.T) {
	// The same registered knob controlhttp reads in forceNoise443().
	forceNoise443 := envknob.RegisterBool(forceNoise443Env)
	if !forceNoise443() {
		t.Fatal("package init did not enable Noise over 443")
	}
	t.Cleanup(func() { envknob.Setenv(forceNoise443Env, "true") })

	envknob.Setenv(forceNoise443Env, "false")
	setForceNoise443Default(func(string) (string, bool) { return "false", true })
	if forceNoise443() {
		t.Fatal("explicit TS_FORCE_NOISE_443=false was overridden")
	}

	setForceNoise443Default(func(string) (string, bool) { return "", false })
	if !forceNoise443() {
		t.Fatal("unset knob was not defaulted to true")
	}
}
