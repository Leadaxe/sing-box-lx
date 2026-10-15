//go:build with_utls

package tls

// lx:begin SPEC 123 random/randomized fingerprints with the hybrid share
//
// REALITY on Xray >= v26.9.8 accepts a ClientHello only with an X25519MLKEM768
// key share (SPEC 083), and gRPC/XHTTP over h2 need ALPN. Both "random" and
// "randomized" are picked once per process, so a pick without them leaves every
// such node dead until a restart:
//   - "random" chose among chrome/firefox/edge/safari/ios, and edge and ios carry
//     no hybrid share (2 of 5 processes);
//   - "randomized" (utls.HelloRandomized) decided the hybrid group, the hybrid
//     share and ALPN by three independent coins (~25% of processes alive for
//     REALITY + gRPC/XHTTP to a new Xray).
// lxRandomFingerprints re-picks both after upstream's init: "random" from the
// presets that carry the hybrid share, "randomized" as HelloRandomizedALPN (as
// Xray does) with the hybrid group and share always on. Everything else in
// "randomized" stays random.

import (
	"math/rand"

	utls "github.com/metacubex/utls"
)

// lxRandomPool is the "random" pool: the presets with an X25519MLKEM768 share
// (chrome 133; firefox 148 and safari 26.3 from the utls fork).
var lxRandomPool = []utls.ClientHelloID{
	utls.HelloChrome_Auto,
	utls.HelloFirefox_Auto,
	utls.HelloSafari_Auto,
}

func lxRandomFingerprints() {
	randomFingerprint = lxRandomPool[rand.Intn(len(lxRandomPool))]
	randomizedFingerprint = lxRandomizedFingerprint(randomizedFingerprint.Seed)
}

// lxRandomizedFingerprint builds "randomized" for a seed: ALPN always, TLS 1.3,
// X25519 first share, X25519MLKEM768 always in supported_groups and key_share
// (the same coin also adds a P-256 share).
func lxRandomizedFingerprint(seed *utls.PRNGSeed) utls.ClientHelloID {
	weights := utls.DefaultWeights
	weights.TLSVersMax_Set_VersionTLS13 = 1
	weights.FirstKeyShare_Set_CurveP256 = 0
	weights.CurveIDs_Append_X25519 = 1
	weights.KeyShare_Append_RandomGroups = 1
	id := utls.HelloRandomizedALPN
	id.Seed = seed
	id.Weights = &weights
	return id
}

// lx:end SPEC 123
