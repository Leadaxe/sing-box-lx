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
	"net"
	"os"
	"strconv"

	"github.com/sagernet/sing/common/logger"
)

var sniffDecoy = []byte{0x00}

// SniffDecoyEnabled reports whether the decoy is on. Gated by
// LX_QUIC_SNIFF_DECOY until the config form is settled.
func SniffDecoyEnabled() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("LX_QUIC_SNIFF_DECOY"))
	return enabled
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
