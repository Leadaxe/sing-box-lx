package wireguard

// lx: SPEC 151 (formerly 153) — the sniff decoy ahead of the AmneziaWG
// masquerade burst.
//
// The burst ahead of a handshake initiation (the per-handshake ip=quic decoy,
// the static i1..i5 decoys, the Jc junk) exists for the first mile. Over a
// relay that sniffs UDP it breaks the flow: the ip=quic decoy is a real QUIC
// Initial with an SNI, Xray with `destOverride: ["quic"]` and no `routeOnly`
// rewrites the destination of the whole association to that name, and the
// handshake behind it never reaches the WireGuard server (issue #64).
// lx.quic_sniff_decoy with `wireguard` in outbound_type puts its one-byte
// datagram ahead of a burst that starts with a QUIC-shaped packet on the
// paths it selects. There is no retry rule here: handshake retries reuse the
// socket, so the relay has already decided on the flow; the byte goes on
// every handshake the policy applies to. What the server validates — S1–S4
// padding, H1–H4, AWG 3.x header protection, trailers — is untouched: the
// filter only sees what precedes the initiation.

import (
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/wireguard-go/device"
)

// newHandshakeBurstFilter returns the device.HandshakeBurstFunc for an
// AmneziaWG endpoint under policy, or nil when the policy does not cover
// wireguard. path classifies the endpoint's socket on each handshake: a
// kernel socket (no detour, or a detour that ends in a direct outbound) or
// the relay type that carries it; an error leaves the burst as assembled,
// the Send that follows reports the fault.
func newHandshakeBurstFilter(policy option.LXQUICSniffDecoyResolved, path func() (lxdatagram.Path, error), logger logger.Logger) device.HandshakeBurstFunc {
	if !policy.Enabled("wireguard") {
		return nil
	}
	return func(burst [][]byte) [][]byte {
		if len(burst) == 0 || !quicShaped(burst[0]) {
			return burst
		}
		current, err := path()
		if err != nil {
			return burst
		}
		if !lxdatagram.PolicyApplies(policy, current) {
			return burst
		}
		if logger != nil {
			logger.Trace("amneziawg: sniff decoy datagram sent ahead of the masquerade burst")
		}
		return append([][]byte{lxdatagram.SniffDecoy()}, burst...)
	}
}

// quicShaped reports whether a datagram would pass a QUIC sniffer's first
// check: a long header (0x80) with the fixed bit (0x40) set, RFC 9000 §17.2.
// The ip=quic decoy and a static i1 taken from a QUIC capture are; dns, stun,
// sip decoys and random junk are not.
func quicShaped(datagram []byte) bool {
	return len(datagram) > 0 && datagram[0]&0xc0 == 0xc0
}
