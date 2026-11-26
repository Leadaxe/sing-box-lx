package masque

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/log"
)

// lx: SPEC 120 §2.6 — InitialPacketSize = mtu + 51, clamped to [1200, 1452].
func TestOuterInitialPacketSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mtu  uint32
		want uint16
	}{
		{1280, 1331},
		{1420, 1452}, // 1471 clamped to quic-go MaxPacketBufferSize
		{1500, 1452},
		{1100, 1200}, // 1151 clamped to the QUIC minimum
		{0, 1331},    // 0 = "not set": lxmtu applies the masque default 1280
		{1401, 1452},
		{1149, 1200},
		{1150, 1201},
	}
	for _, tc := range cases {
		if got := outerInitialPacketSize(tc.mtu); got != tc.want {
			t.Errorf("outerInitialPacketSize(%d) = %d, want %d", tc.mtu, got, tc.want)
		}
	}
}

// NewOutbound feeds the (defaulted) mtu into the outer quic.Config: the default
// 1280 gives 1331, an explicit mtu is honoured and clamped.
func TestNewOutboundInitialPacketSizeFromMTU(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mtu  uint32
		want uint16
	}{
		{"default mtu", 0, 1331},
		{"explicit 1280", 1280, 1331},
		{"explicit 1420 clamps", 1420, 1452},
		{"explicit 1100 clamps", 1100, 1200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := standardOptions("h3", "https://example.com/.well-known/masque/ip/*/*/")
			options.MTU = tc.mtu
			outbound, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "masque-ips", options)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			defer outbound.(*Outbound).Close()
			if got := outbound.(*Outbound).quicConfig.InitialPacketSize; got != tc.want {
				t.Fatalf("InitialPacketSize = %d, want %d", got, tc.want)
			}
		})
	}
}
