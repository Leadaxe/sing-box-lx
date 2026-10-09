// Package lxmtu aligns the packet size of a node that sits on top of an IP
// tunnel — through `detour` or as a `chain` link — with what that tunnel can
// carry (lx: SPEC 120). Tunnels get their `mtu` lowered, QUIC proxies get an
// `initial_packet_size` (plus `disable_path_mtu_discovery`). Everything is a
// pure computation over the option structs, done once at start; no build tag.
package lxmtu

import (
	"math"
	"net/netip"

	C "github.com/sagernet/sing-box/constant"
)

const (
	// IPUDPv4 / IPUDPv6 are the outer IP+UDP headers a tunnel packet pays on
	// the way to a v4 / v6 server.
	IPUDPv4 = 28
	IPUDPv6 = 48
	// WGOverhead is the WireGuard transport data header (type + receiver +
	// counter + AEAD tag).
	WGOverhead = 32
	// MASQUEDatagramOverhead is the QUIC short header + AEAD tag + DATAGRAM
	// frame + context-id of a CONNECT-IP datagram; upstream's masque-client
	// uses the same `mtu + 51` for its outer QUIC.
	MASQUEDatagramOverhead = 51

	// QUICMinPacketSize / QUICMaxPacketSize bound initial_packet_size: the
	// QUIC minimum and quic-go's PMTUD ceiling.
	QUICMinPacketSize = 1200
	QUICMaxPacketSize = 1452

	// Tunnel defaults when `mtu` is absent.
	DefaultWGMTU        = 1408
	DefaultMASQUEMTU    = 1280
	DefaultTailscaleMTU = 1280

	// Unlimited is the capacity of a node without an IP layer of its own.
	Unlimited = math.MaxInt
)

// Family names used in log reasons.
const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

// Family returns the address family of a server string. A domain or an
// unparseable value is reported as IPv6: the conservative choice, as the
// original chain logic made it.
func Family(server string) string {
	if addr, err := netip.ParseAddr(server); err == nil && addr.Unmap().Is4() {
		return FamilyIPv4
	}
	return FamilyIPv6
}

// IPUDP returns the outer IP+UDP overhead towards server.
func IPUDP(server string) int {
	if Family(server) == FamilyIPv4 {
		return IPUDPv4
	}
	return IPUDPv6
}

// IsTunnelType reports whether typeName is an IP tunnel: its packets carry an
// inner IP layer, so its mtu is both a capacity for nodes above and a demand
// on nodes below.
func IsTunnelType(typeName string) bool {
	switch typeName {
	case C.TypeWireGuard, C.TypeMASQUE, C.TypeOpenVPNClient, C.TypeOpenConnect:
		return true
	}
	return false
}

// IsQUICType reports whether typeName is a QUIC proxy whose packet size is
// controlled through QUICOptions.InitialPacketSize.
func IsQUICType(typeName string) bool {
	switch typeName {
	case C.TypeHysteria2, C.TypeTUIC, C.TypeHysteria:
		return true
	}
	return false
}

// TunnelDefaultMTU is the mtu a tunnel type uses when the key is absent;
// 0 when the default is not known to us (openvpn, openconnect).
func TunnelDefaultMTU(typeName string) uint32 {
	switch typeName {
	case C.TypeWireGuard:
		return DefaultWGMTU
	case C.TypeMASQUE:
		return DefaultMASQUEMTU
	}
	return 0
}

// TunnelOverhead is what a tunnel of typeName adds to each inner IP packet
// on the way to server: WireGuard 60 / 80, MASQUE 79 / 99 (v4 / v6). ok is
// false for tunnels whose framing is not modelled (openvpn, openconnect).
func TunnelOverhead(typeName, server string) (overhead int, ok bool) {
	switch typeName {
	case C.TypeWireGuard:
		return WGOverhead + IPUDP(server), true
	case C.TypeMASQUE:
		return MASQUEDatagramOverhead + IPUDP(server), true
	}
	return 0, false
}

// QUICClamp bounds a derived initial_packet_size to [QUICMinPacketSize,
// QUICMaxPacketSize]. raised reports that the limit was below the QUIC
// minimum, i.e. packets of the returned size will not fit the path.
func QUICClamp(limit int) (size int, raised bool) {
	if limit < QUICMinPacketSize {
		return QUICMinPacketSize, true
	}
	if limit > QUICMaxPacketSize {
		return QUICMaxPacketSize, false
	}
	return limit, false
}

// MASQUEOuterInitialPacketSize is the InitialPacketSize of the outer QUIC
// connection of a masque outbound with the given (already aligned) mtu:
// mtu + 51, clamped to the QUIC range (SPEC 120 §2.6).
func MASQUEOuterInitialPacketSize(mtu uint32) uint16 {
	if mtu == 0 {
		mtu = DefaultMASQUEMTU
	}
	size, _ := QUICClamp(int(mtu) + MASQUEDatagramOverhead)
	return uint16(size)
}
