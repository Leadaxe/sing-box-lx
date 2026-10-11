//go:build with_utls

package tls

// lx:begin SPEC 123 tests

import (
	"net"
	"reflect"
	"testing"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func lxALPN(uconn *utls.UConn) []string {
	for _, extension := range uconn.Extensions {
		if alpn, isALPN := extension.(*utls.ALPNExtension); isALPN {
			return alpn.AlpnProtocols
		}
	}
	return nil
}

func lxBuildWithProtos(t *testing.T, id utls.ClientHelloID, protos []string) *utls.UConn {
	t.Helper()
	uconn := utls.UClient(&net.TCPConn{}, &utls.Config{ServerName: "example.com", InsecureSkipVerify: true, NextProtos: protos}, id)
	require.NoError(t, uconn.BuildHandshakeState())
	return uconn
}

// Every seed: X25519MLKEM768 once in supported_groups and once in key_share,
// ahead of X25519; ALPN always present, from NextProtos or h2/http1.1.
func TestLxRandomizedAlwaysHybridAndALPN(t *testing.T) {
	for i := 0; i < 500; i++ {
		seed, err := utls.NewPRNGSeed()
		require.NoError(t, err)
		id := lxRandomizedFingerprint(seed)

		uconn := lxBuildWithProtos(t, id, nil)
		shares := lxKeyShares(t, uconn)
		hybridIndex, hybridCount := lxIndexOfShare(shares, utls.X25519MLKEM768)
		classicalIndex, classicalCount := lxIndexOfShare(shares, utls.X25519)
		require.Equal(t, 1, hybridCount, "seed %d: hybrid share", i)
		require.Equal(t, 1, classicalCount, "seed %d: X25519 share", i)
		require.Less(t, hybridIndex, classicalIndex, "seed %d: hybrid must precede X25519", i)
		groups := lxSupportedGroups(t, uconn)
		require.NotEqual(t, -1, lxIndexOfCurve(groups, utls.X25519MLKEM768), "seed %d: hybrid group", i)
		require.Equal(t, []string{"h2", "http/1.1"}, lxALPN(uconn), "seed %d: default ALPN", i)

		require.Equal(t, []string{"h2"}, lxALPN(lxBuildWithProtos(t, id, []string{"h2"})), "seed %d: ALPN from NextProtos", i)
	}
}

// One seed per process stays deterministic: the same seed gives the same shape.
func TestLxRandomizedSeedDeterministic(t *testing.T) {
	seed, err := utls.NewPRNGSeed()
	require.NoError(t, err)
	shape := func() ([]uint16, []reflect.Type) {
		uconn := lxBuildWithProtos(t, lxRandomizedFingerprint(seed), nil)
		types := make([]reflect.Type, 0, len(uconn.Extensions))
		for _, extension := range uconn.Extensions {
			types = append(types, reflect.TypeOf(extension))
		}
		return uconn.HandshakeState.Hello.CipherSuites, types
	}
	suitesA, typesA := shape()
	suitesB, typesB := shape()
	require.Equal(t, suitesA, suitesB)
	require.Equal(t, typesA, typesB)
}

// "random" picks only presets with the hybrid share; the process pick is one of them.
func TestLxRandomPoolCarriesHybrid(t *testing.T) {
	for _, id := range lxRandomPool {
		hybridIndex, _ := lxIndexOfShare(lxKeyShares(t, lxBuildClientHello(t, id)), utls.X25519MLKEM768)
		require.NotEqual(t, -1, hybridIndex, "%s has no hybrid share", id.Str())
	}
	require.Contains(t, lxRandomPool, randomFingerprint)
	got, err := uTLSClientHelloID("randomized")
	require.NoError(t, err)
	require.Equal(t, utls.HelloRandomizedALPN.Client, got.Client)
	require.Equal(t, 1.0, got.Weights.KeyShare_Append_RandomGroups)
}

// lx:end SPEC 123
