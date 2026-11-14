package lxsniffguard

import (
	"net/netip"
	"strings"
)

// SNIMismatch reports whether the name a node sends in its ClientHello or
// Initial differs from the host of its server, so that a sniffing relay
// would send it elsewhere. server is the node's server address (domain or
// IP, IPv6 with or without brackets); sni is the name that actually goes on
// the wire (the outer, public name under ECH; "" when none is sent).
//
// Names compare case-insensitively with a trailing dot ignored; a subdomain
// is a different name. No SNI is a match (nothing to sniff), so is an IP
// server without a name; an IP server with a name is a mismatch.
//
// Cases decided by the node kind rather than by the names are the caller's:
// REALITY, masque with the WARP profile and the AmneziaWG QUIC masquerade
// are always a mismatch; disable_sni and hysteria2 with Salamander are
// always a match. The caller sets NodeInfo.SNIMismatch directly for them.
func SNIMismatch(server, sni string) bool {
	if sni == "" {
		return false
	}
	return normalizeHost(server) != normalizeHost(sni)
}

func normalizeHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
