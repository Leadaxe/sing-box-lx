package lxdatagram

// lx: SPEC 060 — lx.sniff_guard on a QUIC handshake.
//
// A proxy relay that sniffs UDP to rewrite the destination by the SNI it finds
// (Xray `sniffing` with `destOverride` and no `routeOnly`) decides on the
// first datagram of a flow: Xray drops its QUIC sniffer from the flow once the
// leading bytes are not a QUIC long-header packet, and otherwise reassembles
// the ClientHello from the Initial packets it sees within its wait budget.
// Two methods leave the destination as the client dialled it:
//
//   - quic.decoy_first_byte: one datagram no sniffer recognises, sent before
//     the ClientHello. The server discards it unparsed: a QUIC packet must
//     have the fixed bit (0x40) set (RFC 9000 §17.2, §17.3), and a single byte
//     is too short to draw a stateless reset.
//   - quic.delay: the second Initial datagram is held back past the sniffer's
//     budget (delay.go).
//
// Whether a handshake is protected is the decision of the lx.sniff_guard
// label store (common/lxsniffguard), taken once per QUIC handshake by the
// seam that opens it.

import (
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
)

var sniffDecoy = []byte{0x00}

// SniffDecoy returns the decoy datagram, for a sender that assembles its own
// burst (the AmneziaWG handshake, transport/wireguard).
func SniffDecoy() []byte {
	return append([]byte(nil), sniffDecoy...)
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

// GuardPath returns the lx.sniff_guard path of a freshly dialled conn, as
// dialled and before any wrapper of the caller: a kernel socket is the direct
// path, otherwise the carrier the dial recorded in record; neither is an
// unknown path, which the guard leaves alone.
func GuardPath(conn any, record *adapter.CarrierRecord) lxsniffguard.Path {
	if kernelUnder(conn) {
		return lxsniffguard.Path{Direct: true}
	}
	if record != nil && record.Tag != "" {
		return lxsniffguard.Path{CarrierType: record.Type, CarrierTag: record.Tag}
	}
	return lxsniffguard.Path{}
}

// QUICHandshake is the lx.sniff_guard form of one QUIC handshake. A nil
// *QUICHandshake is a handshake the guard does not touch.
type QUICHandshake struct {
	attempt  *lxsniffguard.Attempt
	decision lxsniffguard.Decision
	delay    *InitialDelayConn
}

// GuardQUIC takes the decision of one QUIC handshake on attempt and applies
// it. dialled is the conn as dialled and record the carrier record of its
// dial (they give the path); conn is the conn the QUIC stack will write to,
// after the caller's own wrappers. The returned conn is the one the
// handshake must use. With a nil attempt conn is returned untouched.
//
// Call Done once the handshake returned, whatever its result, before the
// attempt is settled.
func GuardQUIC(attempt *lxsniffguard.Attempt, dialled any, record *adapter.CarrierRecord, conn net.Conn, logger logger.Logger) (net.Conn, *QUICHandshake) {
	if attempt == nil {
		return conn, nil
	}
	handshake := &QUICHandshake{attempt: attempt}
	handshake.decision = attempt.Decide(lxsniffguard.ProtoQUIC, GuardPath(dialled, record), true)
	if handshake.decision.Protect && handshake.decision.Method == option.LXSniffGuardQUICDecoy {
		WriteSniffDecoy(conn, logger)
	}
	if attempt.Method(lxsniffguard.ProtoQUIC) == option.LXSniffGuardDelay {
		// Watched with or without protection: a ClientHello in one Initial
		// leaves the method nothing to hold back on this node either way.
		var hold time.Duration
		if handshake.decision.Protect {
			hold = handshake.decision.Delay
		}
		handshake.delay = NewInitialDelayConn(conn, hold, logger)
		conn = handshake.delay
	}
	return conn, handshake
}

// Done closes the handshake for the store: when quic.delay found no second
// Initial in the first flight, the method does not apply to this node and
// the attempt's mark is withdrawn.
func (h *QUICHandshake) Done() {
	if h == nil || h.delay == nil || h.delay.SecondInitial() {
		return
	}
	h.attempt.Withdraw()
}

// Report hands an outcome learnt after the dial returned to the store
// (hysteria2, tuic: the 30-second window). A handshake quic.delay could not
// shape is withdrawn instead.
func (h *QUICHandshake) Report(ok bool) {
	if h == nil {
		return
	}
	if h.delay != nil && !h.delay.SecondInitial() {
		h.attempt.Withdraw()
		return
	}
	h.attempt.Report(ok)
}
