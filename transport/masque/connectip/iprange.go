package connectip

// Vendored from connect-ip-go (MIT). IP range → CIDR prefix expansion.

import "net/netip"

// rangeToPrefixes converts an IP range defined by start and end addresses into a
// slice of CIDR prefixes that exactly cover the range. It assumes start and end
// are the same IP version and start <= end.
func rangeToPrefixes(start, end netip.Addr) []netip.Prefix {
	var prefixes []netip.Prefix
	for current := start; current.Compare(end) <= 0; {
		prefix := findLargestPrefix(current, end)
		prefixes = append(prefixes, prefix)

		lastIP := lastIPInPrefix(prefix)
		if lastIP.Compare(end) >= 0 {
			break
		}
		current = lastIP.Next()
	}
	return prefixes
}

func findLargestPrefix(start, end netip.Addr) netip.Prefix {
	if start == end {
		return netip.PrefixFrom(start, start.BitLen())
	}

	var prefixLen int
	for prefixLen = start.BitLen(); prefixLen > 0; prefixLen-- {
		prefix := netip.PrefixFrom(start, prefixLen-1)
		if lastIPInPrefix(prefix).Compare(end) > 0 || !isAligned(start, prefixLen-1) {
			break
		}
	}
	return netip.PrefixFrom(start, prefixLen)
}

func lastIPInPrefix(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr()
	bits := addr.As16()

	hostBits := addr.BitLen() - prefix.Bits()

	for i := len(bits) - 1; i >= 0 && hostBits > 0; i-- {
		bitsInThisByte := min(8, hostBits)
		mask := byte((1 << bitsInThisByte) - 1)
		bits[i] |= mask
		hostBits -= bitsInThisByte
	}

	if addr.Is4() {
		return netip.AddrFrom4([4]byte(bits[12:16]))
	}
	return netip.AddrFrom16(bits)
}

func isAligned(addr netip.Addr, prefixLen int) bool {
	bits := addr.As16()

	hostBits := addr.BitLen() - prefixLen
	for i := len(bits) - 1; i >= 0 && hostBits > 0; i-- {
		bitsInThisByte := min(8, hostBits)
		mask := byte((1 << bitsInThisByte) - 1)
		if bits[i]&mask != 0 {
			return false
		}
		hostBits -= bitsInThisByte
	}
	return true
}
