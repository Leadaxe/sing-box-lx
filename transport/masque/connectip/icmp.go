package connectip

// Vendored from connect-ip-go (MIT). Builds an ICMP "packet too big" reply when
// an outgoing IP packet exceeds the tunnel's datagram size.

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func composeICMPTooLargePacket(b []byte, mtu int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("connect-ip: empty packet")
	}

	var icmpMessage *icmp.Message
	var psh []byte
	switch v := ipVersion(b); v {
	case 4:
		if len(b) < ipv4.HeaderLen {
			return nil, errors.New("connect-ip: IPv4 packet too short")
		}
		icmpMessage = &icmp.Message{
			Type: ipv4.ICMPTypeDestinationUnreachable,
			Code: 4, // Fragmentation Needed and Don't Fragment was Set
			Body: &icmp.PacketTooBig{
				MTU:  mtu,
				Data: b[:min(len(b), ipv4.HeaderLen+8)],
			},
		}
	case 6:
		if len(b) < ipv6.HeaderLen {
			return nil, errors.New("connect-ip: IPv6 packet too short")
		}
		icmpMessage = &icmp.Message{
			Type: ipv6.ICMPTypePacketTooBig,
			Body: &icmp.PacketTooBig{
				MTU: mtu,
				// 1232 = minMTU(1280) − IPv6 header(40) − ICMPv6 header(8): the most
				// quoted data that keeps the ICMPv6 reply within the min IPv6 MTU (RFC 4443).
				Data: b[:min(len(b), 1232)],
			},
		}
		psh = icmp.IPv6PseudoHeader(b[24:40], b[8:24])
	default:
		return nil, fmt.Errorf("connect-ip: unknown IP version: %d", v)
	}

	icmpBytes, err := icmpMessage.Marshal(psh)
	if err != nil {
		return nil, fmt.Errorf("connect-ip: failed to marshal ICMP message: %w", err)
	}

	if ipVersion(b) == 4 {
		var header [ipv4.HeaderLen]byte
		header[0] = 4<<4 | ipv4.HeaderLen>>2 // Version and IHL
		ipLen := ipv4.HeaderLen + len(icmpBytes)
		binary.BigEndian.PutUint16(header[2:4], uint16(ipLen)) // Total Length
		header[8] = 64                                         // TTL
		header[9] = 1                                          // Protocol (ICMP)
		copy(header[12:16], b[16:20])                          // Source IP (swapped)
		copy(header[16:20], b[12:16])                          // Dest IP (swapped)
		binary.BigEndian.PutUint16(header[10:12], calculateIPv4Checksum(header[:]))
		return append(header[:], icmpBytes...), nil
	}

	var header [ipv6.HeaderLen]byte
	header[0] = 6 << 4                                              // Version 6
	binary.BigEndian.PutUint16(header[4:6], uint16(len(icmpBytes))) // Payload Length
	header[6] = 58                                                  // Next Header (ICMPv6)
	header[7] = 64                                                  // Hop Limit
	copy(header[8:24], b[24:40])                                    // Source IP (swapped)
	copy(header[24:40], b[8:24])                                    // Dest IP (swapped)
	return append(header[:], icmpBytes...), nil
}
