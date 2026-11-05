package wireguard

// lx: SPEC 153 — the AmneziaWG masquerade burst by path.
//
// The burst ahead of a handshake initiation (the per-handshake ip=quic decoy,
// the static i1..i5 decoys, the Jc junk) exists for the first mile. Over a
// detour the first mile sees the carrying protocol, never the burst, so the
// burst protects nothing there and breaks the flow through a relay that
// sniffs UDP: the ip=quic decoy is a real QUIC Initial with an SNI, Xray with
// `destOverride: ["quic"]` and no `routeOnly` rewrites the destination of the
// whole association to that name, and the handshake behind it never reaches
// the WireGuard server (issue #64). lx.awg_masquerade decides where the burst
// is sent: on every path by default, "direct" keeps it off a detour.
// lx.quic_sniff_decoy (SPEC 151) puts its one-byte datagram ahead of a burst
// that starts with a QUIC-shaped packet on the paths it selects. What the
// server validates — S1–S4 padding, H1–H4, AWG 3.x header protection,
// trailers — is untouched: the filter only sees what precedes the initiation.

import (
	"sync/atomic"

	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/wireguard-go/device"
)

// newHandshakeBurstFilter returns the device.HandshakeBurstFunc for an
// AmneziaWG endpoint under the lx.awg_masquerade mode masquerade and the
// lx.quic_sniff_decoy mode sniffDecoy, or nil when neither can change the
// burst. pathIsKernel classifies the endpoint's socket on each handshake:
// true for a kernel socket (no detour, or a detour that ends in a direct
// outbound, lxdatagram/kernel.go),
// false for a conn carried by another outbound; an error leaves the burst as
// assembled, the Send that follows reports the fault.
func newHandshakeBurstFilter(masquerade, sniffDecoy string, pathIsKernel func() (bool, error), logger logger.Logger) device.HandshakeBurstFunc {
	stripOverDetour := masquerade == option.LXAWGMasqueradeDirect
	decoyEver := lxdatagram.SniffDecoyAppliesTo(sniffDecoy, true) || lxdatagram.SniffDecoyAppliesTo(sniffDecoy, false)
	if !stripOverDetour && !decoyEver {
		return nil
	}
	var stripReported atomic.Bool
	return func(burst [][]byte) [][]byte {
		if len(burst) == 0 {
			return nil
		}
		kernel, err := pathIsKernel()
		if err != nil {
			return burst
		}
		if stripOverDetour && !kernel {
			if logger != nil && stripReported.CompareAndSwap(false, true) {
				logger.Debug("amneziawg: masquerade burst of ", len(burst), " datagrams not sent over the detour (lx.awg_masquerade: direct)")
			}
			return nil
		}
		if lxdatagram.SniffDecoyAppliesTo(sniffDecoy, kernel) && quicShaped(burst[0]) {
			if logger != nil {
				logger.Trace("amneziawg: sniff decoy datagram sent ahead of the masquerade burst")
			}
			return append([][]byte{lxdatagram.SniffDecoy()}, burst...)
		}
		return burst
	}
}

// quicShaped reports whether a datagram would pass a QUIC sniffer's first
// check: a long header (0x80) with the fixed bit (0x40) set, RFC 9000 §17.2.
// The ip=quic decoy and a static i1 taken from a QUIC capture are; dns, stun,
// sip decoys and random junk are not.
func quicShaped(datagram []byte) bool {
	return len(datagram) > 0 && datagram[0]&0xc0 == 0xc0
}
