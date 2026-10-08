//go:build with_utls

package tls

import (
	"net"
	"testing"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 086 (and the SPEC 083 §6 merge check). REALITY on Xray >= v26.9.8
// (XTLS/REALITY@8cdf7bf) accepts a ClientHello only when it carries an X25519MLKEM768
// key_share ahead of the optional X25519 one, each at most once. SPEC 083 stopped the
// client from cutting that share out; whether a fingerprint carries it at all is decided
// by the utls preset. `chrome` had it in metacubex/utls v1.8.7 already; `firefox` and
// `safari` get it from the fork submodule (submodules/utls: Firefox 148 + key share reuse,
// SPEC 086, and Safari 26.3, SPEC 087, ported from refraction-networking/utls). The fork
// also carries Chrome 155 as the opt-in `chrome_155` name (SPEC 118); `chrome` stays Chrome
// 133. These tests pin what the presets send, so a utls bump that drops the hybrid share,
// reorders it behind X25519, moves `chrome` off 133, or loses the Chrome 155 / Firefox 148 /
// Safari 26.3 presets fails here instead of in the field as a silent
// `reality verification failed`.

func lxBuildClientHello(t *testing.T, id utls.ClientHelloID) *utls.UConn {
	t.Helper()
	uconn := utls.UClient(&net.TCPConn{}, &utls.Config{ServerName: "example.com", InsecureSkipVerify: true}, id)
	require.NoError(t, uconn.BuildHandshakeState())
	return uconn
}

func lxKeyShares(t *testing.T, uconn *utls.UConn) []utls.KeyShare {
	t.Helper()
	for _, extension := range uconn.Extensions {
		if keyShare, isKeyShare := extension.(*utls.KeyShareExtension); isKeyShare {
			return keyShare.KeyShares
		}
	}
	t.Fatal("key_share extension not found")
	return nil
}

func lxSupportedGroups(t *testing.T, uconn *utls.UConn) []utls.CurveID {
	t.Helper()
	for _, extension := range uconn.Extensions {
		if curves, isCurves := extension.(*utls.SupportedCurvesExtension); isCurves {
			return curves.Curves
		}
	}
	t.Fatal("supported_groups extension not found")
	return nil
}

func lxIndexOfShare(shares []utls.KeyShare, group utls.CurveID) (index, count int) {
	index = -1
	for i, share := range shares {
		if share.Group == group {
			if index == -1 {
				index = i
			}
			count++
		}
	}
	return index, count
}

func lxIndexOfCurve(curves []utls.CurveID, group utls.CurveID) int {
	for i, curve := range curves {
		if curve == group {
			return i
		}
	}
	return -1
}

// The `chrome` name (and the empty default) resolves to HelloChrome_Auto, which the fork
// keeps at Chrome 133 (SPEC 118): Chrome 155 advertises ML-DSA signatures the fork cannot
// verify and trust_anchors, so it is not forced on every default user.
func TestLxChromeFingerprintStaysChrome133(t *testing.T) {
	for _, name := range []string{"chrome", ""} {
		id, err := uTLSClientHelloID(name)
		require.NoError(t, err)
		require.Equal(t, utls.HelloChrome_Auto, id)
		require.Equal(t, utls.HelloChrome_133, id)
		require.Equal(t, "133", id.Version)
	}
}

// Chrome 155 is reachable only under the explicit `chrome_155` name (SPEC 118).
func TestLxChrome155FingerprintIsOptIn(t *testing.T) {
	id, err := uTLSClientHelloID("chrome_155")
	require.NoError(t, err)
	require.Equal(t, utls.HelloChrome_155, id)
	require.Equal(t, "155", id.Version)
}

// The `firefox` name resolves to HelloFirefox_Auto, which the fork's port makes
// Firefox 148 (SPEC 086 R3). A metacubex/utls without the port would leave it at
// HelloFirefox_120 — no hybrid share, dead on Xray >= v26.9.8.
func TestLxFirefoxFingerprintIsFirefox148(t *testing.T) {
	id, err := uTLSClientHelloID("firefox")
	require.NoError(t, err)
	require.Equal(t, utls.HelloFirefox_Auto, id)
	require.Equal(t, utls.HelloFirefox_148, id)
	require.Equal(t, "148", id.Version)
}

// The `safari` name resolves to HelloSafari_Auto, which the fork's port makes Safari 26.3
// (SPEC 087). A metacubex/utls without the port would leave it at HelloSafari_16_0 — no
// hybrid share, dead on Xray >= v26.9.8.
func TestLxSafariFingerprintIsSafari26_3(t *testing.T) {
	id, err := uTLSClientHelloID("safari")
	require.NoError(t, err)
	require.Equal(t, utls.HelloSafari_Auto, id)
	require.Equal(t, utls.HelloSafari_26_3, id)
	require.Equal(t, "26.3", id.Version)
}

// The presets the fork stands on for REALITY send X25519MLKEM768 before X25519,
// each exactly once, in key_share and in supported_groups — the server's acceptance rule.
func TestLxRealityFingerprintsCarryHybridShareFirst(t *testing.T) {
	for _, name := range []string{"chrome", "chrome_155", "firefox", "safari"} {
		t.Run(name, func(t *testing.T) {
			id, err := uTLSClientHelloID(name)
			require.NoError(t, err)
			uconn := lxBuildClientHello(t, id)

			shares := lxKeyShares(t, uconn)
			hybridIndex, hybridCount := lxIndexOfShare(shares, utls.X25519MLKEM768)
			classicalIndex, classicalCount := lxIndexOfShare(shares, utls.X25519)
			require.NotEqual(t, -1, hybridIndex, "no X25519MLKEM768 key_share")
			require.NotEqual(t, -1, classicalIndex, "no X25519 key_share")
			require.Less(t, hybridIndex, classicalIndex, "X25519MLKEM768 must precede X25519")
			require.Equal(t, 1, hybridCount, "X25519MLKEM768 must appear once")
			require.Equal(t, 1, classicalCount, "X25519 must appear once")
			require.Len(t, shares[hybridIndex].Data, 1184+32, "hybrid share = ML-KEM-768 encapsulation key + X25519 public key")
			require.Len(t, shares[classicalIndex].Data, 32)

			groups := lxSupportedGroups(t, uconn)
			hybridGroup := lxIndexOfCurve(groups, utls.X25519MLKEM768)
			classicalGroup := lxIndexOfCurve(groups, utls.X25519)
			require.NotEqual(t, -1, hybridGroup, "X25519MLKEM768 missing from supported_groups")
			require.NotEqual(t, -1, classicalGroup, "X25519 missing from supported_groups")
			require.Less(t, hybridGroup, classicalGroup)
		})
	}
}

// What makes the preset a real Firefox 148 rather than a Firefox 120 with an extra
// share: the X25519 half of the hybrid share and the standalone X25519 share are one
// key, and the private key REALITY derives AuthKey from (Ecdhe) is that same key
// (SPEC 086 acceptance criterion 3). This is the library-side reuse logic that could
// not be built in our layer — a metacubex/utls without it would send two independent
// keys here.
func TestLxFirefox148ReusesClassicalKeyAcrossShares(t *testing.T) {
	uconn := lxBuildClientHello(t, utls.HelloFirefox_148)
	shares := lxKeyShares(t, uconn)
	hybridIndex, _ := lxIndexOfShare(shares, utls.X25519MLKEM768)
	classicalIndex, _ := lxIndexOfShare(shares, utls.X25519)
	require.NotEqual(t, -1, hybridIndex)
	require.NotEqual(t, -1, classicalIndex)
	hybrid := shares[hybridIndex].Data
	classical := shares[classicalIndex].Data
	require.Len(t, classical, 32)
	require.Equal(t, classical, hybrid[len(hybrid)-32:], "X25519 half of the hybrid share must be the standalone X25519 share")

	keys := uconn.HandshakeState.State13.KeyShareKeys
	require.NotNil(t, keys)
	require.NotNil(t, keys.Ecdhe)
	require.NotNil(t, keys.MlkemEcdhe)
	require.Same(t, keys.Ecdhe, keys.MlkemEcdhe, "hybrid and classical shares must share one private key")
	require.Equal(t, classical, keys.Ecdhe.PublicKey().Bytes())
}
