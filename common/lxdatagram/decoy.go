package lxdatagram

// lx: SPEC 151 — a decoy datagram ahead of the QUIC Initial.
//
// A proxy relay that sniffs UDP to rewrite the destination by the SNI it finds
// (Xray `sniffing` with `destOverride` and no `routeOnly`, sing-box `sniff`
// with `sniff_override_destination`) decides on the first datagram of a flow:
// Xray drops its QUIC sniffer from the flow once the leading bytes are not a
// QUIC long-header packet, sing-box records the sniff error and skips every
// later packet. One datagram that no sniffer recognises, sent before the
// ClientHello, leaves the destination as the client dialled it. The server
// discards it unparsed: a QUIC packet must have the fixed bit (0x40) set
// (RFC 9000 §17.2, §17.3), and a single byte is too short to draw a
// stateless reset.

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

var sniffDecoy = []byte{0x00}

// SniffDecoyFromContext returns the lx.quic_sniff_decoy mode for outbounds
// built in ctx: "off" (absent), "direct", "detour" or "all".
func SniffDecoyFromContext(ctx context.Context) string {
	return service.PtrFromContext[option.LXResolved](ctx).QUICSniffDecoyOrOff()
}

// SniffDecoyApplies reports whether a dial whose freshly dialled conn is conn
// carries the decoy under mode. "direct" means a kernel socket under the QUIC
// stack (no detour, or a detour to a direct outbound), "detour" means a conn
// carried by another outbound — a proxy or a tunnel. Pass the conn as dialled,
// before any wrapper.
func SniffDecoyApplies(mode string, conn net.Conn) bool {
	return SniffDecoyAppliesTo(mode, IsKernelSocket(conn))
}

// SniffDecoyAppliesTo is SniffDecoyApplies for a path already classified:
// kernel is true for a kernel socket, false for a conn carried by another
// outbound. The WireGuard bind classifies its own socket (lx: SPEC 153).
func SniffDecoyAppliesTo(mode string, kernel bool) bool {
	switch mode {
	case option.LXQUICSniffDecoyAll:
		return true
	case option.LXQUICSniffDecoyDirect:
		return kernel
	case option.LXQUICSniffDecoyDetour:
		return !kernel
	}
	return false
}

// SniffDecoy returns the decoy datagram, for a sender that assembles its own
// burst (the AmneziaWG handshake, lx: SPEC 153).
func SniffDecoy() []byte {
	return append([]byte(nil), sniffDecoy...)
}

// AWGMasqueradeFromContext returns the lx.awg_masquerade mode for endpoints
// built in ctx: "all" (absent) or "direct" (lx: SPEC 153).
func AWGMasqueradeFromContext(ctx context.Context) string {
	return service.PtrFromContext[option.LXResolved](ctx).AWGMasqueradeOrAll()
}

// WriteSniffDecoy sends the decoy datagram on a freshly dialled QUIC conn,
// before the QUIC stack writes its first Initial. A failed write is logged and
// otherwise ignored: the decoy is advisory and the handshake reports any real
// fault of the conn itself.
func WriteSniffDecoy(conn net.Conn, logger logger.Logger) {
	_, err := conn.Write(sniffDecoy)
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debug("sniff decoy datagram not sent: ", err)
	} else {
		logger.Trace("sniff decoy datagram sent ahead of the QUIC Initial")
	}
}
