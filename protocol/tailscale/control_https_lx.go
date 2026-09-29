//go:build with_gvisor

package tailscale

import (
	"os"

	"github.com/sagernet/tailscale/envknob"
)

// lx: SPEC 111 — Tailscale dials the control plane over plain HTTP on port 80
// first and upgrades the connection to Noise. Middleboxes that freeze port-80
// flows after the upgrade leave the map long-poll on a dead connection:
// tailscale keeps reusing it and falls back to 443 only after the kernel
// gives up on the socket (~15 min). Default to Noise over HTTPS unless the
// knob is set explicitly.
const forceNoise443Env = "TS_FORCE_NOISE_443"

func init() {
	setForceNoise443Default(os.LookupEnv)
}

func setForceNoise443Default(lookupEnv func(string) (string, bool)) {
	if _, loaded := lookupEnv(forceNoise443Env); loaded {
		return
	}
	envknob.Setenv(forceNoise443Env, "true")
}
