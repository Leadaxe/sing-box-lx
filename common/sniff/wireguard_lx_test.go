package sniff_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/sniff"
	C "github.com/sagernet/sing-box/constant"

	"github.com/stretchr/testify/require"
)

// Real handshake initiation built with throwaway keys (Noise IK, 148 B, plain WG).
const wgInitiationHex = "010000006c3b96c0cdcce89a026bbdd1dbc334bab681b882ffd4b0dc4d7d12f673804d78ed74a77fe4d81705b89936b6830b48b4218ecdbdba94f7c4493ace64394e20c92b5fe21d45b186d9f3441cdb8e84982f57289f503d662f2021ac08b07104a7b848b94152ebd6da4d71dc509f9bf92898f67e88cd283a3c2aec2995bb59cb76d300000000000000000000000000000000"

// Same packet with Cloudflare-WARP-style non-zero reserved bytes.
const wgInitiationWARPHex = "01abcdef6c3b96c0cdcce89a026bbdd1dbc334bab681b882ffd4b0dc4d7d12f673804d78ed74a77fe4d81705b89936b6830b48b4218ecdbdba94f7c4493ace64394e20c92b5fe21d45b186d9f3441cdb8e84982f57289f503d662f2021ac08b07104a7b848b94152ebd6da4d71dc509f9bf92898f67e88cd283a3c2aec2995bb59cb76d300000000000000000000000000000000"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func sized(first byte, n int) []byte {
	b := make([]byte, n)
	b[0] = first
	for i := 4; i < n; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

func TestSniffWireGuard(t *testing.T) {
	t.Parallel()
	for name, pkt := range map[string][]byte{
		"initiation":        mustHex(t, wgInitiationHex),
		"initiation-warp":   mustHex(t, wgInitiationWARPHex),
		"response":          sized(2, 92),
		"cookie":            sized(3, 64),
		"keepalive":         sized(4, 32),
		"transport-48":      sized(4, 48),
		"transport-1312":    sized(4, 1312),
		"transport-1280+32": sized(4, 1280+32),
	} {
		var metadata adapter.InboundContext
		require.NoError(t, sniff.WireGuard(context.TODO(), &metadata, pkt), name)
		require.Equal(t, C.ProtocolWireGuard, metadata.Protocol, name)
	}
}

func TestSniffNotWireGuard(t *testing.T) {
	t.Parallel()
	for name, pkt := range map[string][]byte{
		"short":                 {1, 0, 0},
		"initiation-wrong-size": sized(1, 147),
		"response-wrong-size":   sized(2, 93),
		"cookie-wrong-size":     sized(3, 60),
		"transport-too-short":   sized(4, 31),
		"transport-unaligned":   sized(4, 33),
		"unknown-type":          sized(5, 148),
		"utp-syn":               mustHex(t, "410277ef0b1fb1f60000000000040000c233000000080000000000000000"),
		"utp-state":             mustHex(t, "21001ecb6817f2805d044fd700100000dbd03029"),
		"stun-binding":          mustHex(t, "000100002112a442"+"000000000000000000000000"),
	} {
		var metadata adapter.InboundContext
		require.Error(t, sniff.WireGuard(context.TODO(), &metadata, pkt), name)
		require.Empty(t, metadata.Protocol, name)
	}
}

// Documents the upstream false positive this sniffer exists for, and that
// the default order (WireGuard before UTP) resolves it.
func TestSniffWireGuardBeforeUTP(t *testing.T) {
	t.Parallel()
	pkt := mustHex(t, wgInitiationHex)

	var utpOnly adapter.InboundContext
	require.NoError(t, sniff.UTP(context.TODO(), &utpOnly, pkt), "uTP sniffer accepts a WG initiation (upstream heuristic)")
	require.Equal(t, C.ProtocolBitTorrent, utpOnly.Protocol)

	var ordered adapter.InboundContext
	require.NoError(t, sniff.PeekPacket(context.TODO(), &ordered, pkt, sniff.WireGuard, sniff.UTP))
	require.Equal(t, C.ProtocolWireGuard, ordered.Protocol)
}
