package masque

import "github.com/sagernet/sing-box/common/lxmtu"

// lx: SPEC 120 §2.6 — the outer QUIC connection starts from the tunnel MTU,
// not from a constant.
//
// quic-go sizes the datagram budget conservatively: InitialPacketSize − 37
// (short header type byte + 20-byte connection ID + 16-byte AEAD tag, see
// connection.go estimateMaxPayloadSize). With the old constant 1242 (taken
// from usque) that budget was 1205 bytes, while a full inner packet of the
// default MTU 1280 plus the one-byte context-id needs 1281. The budget only
// grows through path MTU discovery, which starts after the handshake is
// confirmed and sends its first probe about 5 RTT later, so for roughly 6 RTT
// after the tunnel comes up every inner packet of 1205…1280 bytes was bounced
// with DatagramTooLarge, answered by an ICMP "packet too big" naming 1280 (a
// no-op for a stack that already sends ≤ 1280) and silently lost.
//
// Deriving the initial size from the aligned mtu closes that window; masque
// under a WG/AWG endpoint inherits the aligned value for free. The formula
// (mtu + 51, clamped to [1200, 1452]) lives in lxmtu next to the alignment
// tables so the chain/detour side and this outbound agree on the overhead.
func outerInitialPacketSize(mtu uint32) uint16 {
	return lxmtu.MASQUEOuterInitialPacketSize(mtu)
}
