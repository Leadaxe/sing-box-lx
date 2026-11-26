package connectip

// Vendored from connect-ip-go (MIT). IPv4 header checksum recomputation used
// after TTL decrement.

import (
	"encoding/binary"
)

// calculateIPv4Checksum recomputes the IPv4 header checksum over the whole
// header the caller passes (header[:IHL*4]), so options (IHL>5) are covered.
// lx: SPEC 022 #6 — the vendored original took a fixed [20]byte and summed only
// the first 20 bytes, yielding a wrong checksum when options were present. The
// checksum field itself (offset 10-11) is skipped.
func calculateIPv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		if i == 10 {
			continue // skip checksum field
		}
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for (sum >> 16) > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
