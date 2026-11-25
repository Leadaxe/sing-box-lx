package socks

// lx:begin SPEC 138 a SOCKS server that refused the destination
//
// sing's SOCKS client (protocol/socks/handshake.go) returns a plain error for a
// reply with a failure code: "socks4: request rejected, code= N" and
// "socks5: request rejected, code=N". It carries no type, so the reply is told
// by its text, the one place where the server said the destination failed. Codes
// 7 (command not supported) and 8 (address type not supported) are about the
// server, not the destination, and code 1 (general failure) names no cause: it is
// a failure of the node (FAIL_NODE, ALGORITHM.md §2), not of the destination. The
// SOCKS5 codes 2 not allowed by the ruleset, 3 network unreachable, 4 host
// unreachable, 5 connection refused, 6 TTL expired — and the SOCKS4 code 91
// (rejected or failed) are the destination's; the SOCKS4 identd codes 92 and 93 are the client's. Any other
// error (the dial to the server, EOF or a reset during the handshake, a timeout)
// is the node's.

import (
	"net"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
)

const (
	lxSocks5Rejected = "socks5: request rejected, code="
	lxSocks4Rejected = "socks4: request rejected, code="
)

// lxDestinationRefused marks err as the destination's (adapter.DestinationRefusedLx
// with the tag of the outbound) when it is a SOCKS reply with a destination failure
// code.
func (h *Outbound) lxDestinationRefused(conn net.Conn, err error) (net.Conn, error) {
	return lxDestinationRefusedTag(h.Tag(), conn, err)
}

func lxDestinationRefusedTag(tag string, conn net.Conn, err error) (net.Conn, error) {
	if err == nil || !lxSocksDestinationCode(err.Error()) {
		return conn, err
	}
	return conn, &adapter.DestinationRefusedLx{Tag: tag, Err: err}
}

func lxSocksDestinationCode(message string) bool {
	if code, found := lxReplyCode(message, lxSocks4Rejected); found {
		return code == 91
	}
	code, found := lxReplyCode(message, lxSocks5Rejected)
	return found && code >= 2 && code <= 6
}

// lxReplyCode reads the reply code that follows prefix in message.
func lxReplyCode(message string, prefix string) (int, bool) {
	index := strings.Index(message, prefix)
	if index < 0 {
		return 0, false
	}
	code := strings.TrimSpace(message[index+len(prefix):])
	if end := strings.IndexFunc(code, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
		code = code[:end]
	}
	value, err := strconv.Atoi(code)
	return value, err == nil
}

// lx:end SPEC 138
