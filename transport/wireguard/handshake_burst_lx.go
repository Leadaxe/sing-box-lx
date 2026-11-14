package wireguard

// lx: SPEC 060 (formerly 153) — the sniff decoy ahead of the AmneziaWG
// masquerade burst.
//
// The burst ahead of a handshake initiation (the per-handshake ip=quic decoy,
// the static i1..i5 decoys, the Jc junk) exists for the first mile. Over a
// relay that sniffs UDP it breaks the flow: the ip=quic decoy is a real QUIC
// Initial with an SNI, Xray with `destOverride: ["quic"]` and no `routeOnly`
// rewrites the destination of the whole association to that name, and the
// handshake behind it never reaches the WireGuard server (issue #64).
// lx.sniff_guard puts its one-byte datagram ahead of a burst that starts with
// a QUIC-shaped packet when the quic label of the socket's path says protect
// with decoy_first_byte. The relay decides on the first datagram of the flow
// and handshake retries reuse the socket, so the decision is read from the
// label once per socket generation (Node.Peek) and the handshake outcome
// never moves a label. quic.delay does not apply: the burst is not a QUIC
// handshake. What the server validates — S1–S4 padding, H1–H4, AWG 3.x header
// protection, trailers — is untouched: the filter only sees what precedes the
// initiation.

import (
	"sync"

	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/wireguard-go/device"
)

// newHandshakeBurstFilter returns the device.HandshakeBurstFunc for an
// AmneziaWG endpoint whose lx.sniff_guard handle is node, or nil when the
// guard does not cover it. path classifies the endpoint's socket: the path
// (a kernel socket, or the relay that carries it) and the socket generation,
// compared by identity; a nil generation has no identity and is read again
// on every handshake. An error leaves the burst as assembled, the Send that
// follows reports the fault.
func newHandshakeBurstFilter(node *lxsniffguard.Node, path func() (lxsniffguard.Path, any, error), logger logger.Logger) device.HandshakeBurstFunc {
	if node == nil {
		return nil
	}
	var (
		access     sync.Mutex
		generation any
		decoy      bool
	)
	return func(burst [][]byte) [][]byte {
		if len(burst) == 0 || !quicShaped(burst[0]) {
			return burst
		}
		current, socket, err := path()
		if err != nil {
			return burst
		}
		access.Lock()
		if socket == nil || socket != generation {
			decision := node.Peek(lxsniffguard.ProtoQUIC, current)
			decoy = decision.Protect && decision.Method == option.LXSniffGuardQUICDecoy
			generation = socket
		}
		withDecoy := decoy
		access.Unlock()
		if !withDecoy {
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
