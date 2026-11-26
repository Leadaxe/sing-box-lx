package connectip

// Vendored from connect-ip-go (MIT). Address/route capsules of RFC 9484,
// ported onto sagernet/quic-go. See connectip.go header for provenance.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/quic-go/quicvarint"
)

const (
	capsuleTypeAddressAssign      http3.CapsuleType = 1
	capsuleTypeAddressRequest     http3.CapsuleType = 2
	capsuleTypeRouteAdvertisement http3.CapsuleType = 3
)

type addressAssignCapsule struct {
	AssignedAddresses []AssignedAddress
}

// AssignedAddress represents an Assigned Address within an ADDRESS_ASSIGN capsule.
type AssignedAddress struct {
	RequestID uint64
	IPPrefix  netip.Prefix
}

func (a AssignedAddress) len() int {
	return quicvarint.Len(a.RequestID) + 1 + a.IPPrefix.Addr().BitLen()/8 + 1
}

type addressRequestCapsule struct {
	RequestedAddresses []RequestedAddress
}

// RequestedAddress represents a Requested Address within an ADDRESS_REQUEST capsule.
type RequestedAddress struct {
	RequestID uint64
	IPPrefix  netip.Prefix
}

func (r RequestedAddress) len() int {
	return quicvarint.Len(r.RequestID) + 1 + r.IPPrefix.Addr().BitLen()/8 + 1
}

func parseAddressAssignCapsule(r io.Reader) (*addressAssignCapsule, error) {
	var assignedAddresses []AssignedAddress
	for {
		requestID, prefix, err := parseAddress(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		assignedAddresses = append(assignedAddresses, AssignedAddress{RequestID: requestID, IPPrefix: prefix})
	}
	return &addressAssignCapsule{AssignedAddresses: assignedAddresses}, nil
}

func (c *addressAssignCapsule) append(b []byte) []byte {
	totalLen := 0
	for _, addr := range c.AssignedAddresses {
		totalLen += addr.len()
	}

	b = quicvarint.Append(b, uint64(capsuleTypeAddressAssign))
	b = quicvarint.Append(b, uint64(totalLen))

	for _, addr := range c.AssignedAddresses {
		b = quicvarint.Append(b, addr.RequestID)
		if addr.IPPrefix.Addr().Is4() {
			b = append(b, 4)
		} else {
			b = append(b, 6)
		}
		b = append(b, addr.IPPrefix.Addr().AsSlice()...)
		b = append(b, byte(addr.IPPrefix.Bits()))
	}
	return b
}

func parseAddressRequestCapsule(r io.Reader) (*addressRequestCapsule, error) {
	var requestedAddresses []RequestedAddress
	for {
		requestID, prefix, err := parseAddress(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		requestedAddresses = append(requestedAddresses, RequestedAddress{RequestID: requestID, IPPrefix: prefix})
	}
	return &addressRequestCapsule{RequestedAddresses: requestedAddresses}, nil
}

func (c *addressRequestCapsule) append(b []byte) []byte {
	var totalLen int
	for _, addr := range c.RequestedAddresses {
		totalLen += addr.len()
	}

	b = quicvarint.Append(b, uint64(capsuleTypeAddressRequest))
	b = quicvarint.Append(b, uint64(totalLen))

	for _, addr := range c.RequestedAddresses {
		b = quicvarint.Append(b, addr.RequestID)
		if addr.IPPrefix.Addr().Is4() {
			b = append(b, 4)
		} else {
			b = append(b, 6)
		}
		b = append(b, addr.IPPrefix.Addr().AsSlice()...)
		b = append(b, byte(addr.IPPrefix.Bits()))
	}
	return b
}

func parseAddress(r io.Reader) (requestID uint64, prefix netip.Prefix, _ error) {
	vr := quicvarint.NewReader(r)
	requestID, err := quicvarint.Read(vr)
	if err != nil {
		return 0, netip.Prefix{}, err
	}
	ipVer, err := vr.ReadByte()
	if err != nil {
		return 0, netip.Prefix{}, err
	}
	var ip netip.Addr
	switch ipVer {
	case 4:
		var v4 [4]byte
		if _, err := io.ReadFull(r, v4[:]); err != nil {
			return 0, netip.Prefix{}, err
		}
		ip = netip.AddrFrom4(v4)
	case 6:
		var v6 [16]byte
		if _, err := io.ReadFull(r, v6[:]); err != nil {
			return 0, netip.Prefix{}, err
		}
		ip = netip.AddrFrom16(v6)
	default:
		return 0, netip.Prefix{}, fmt.Errorf("invalid IP version: %d", ipVer)
	}
	prefixLen, err := vr.ReadByte()
	if err != nil {
		return 0, netip.Prefix{}, err
	}
	if int(prefixLen) > ip.BitLen() {
		return 0, netip.Prefix{}, fmt.Errorf("prefix length %d exceeds IP address length (%d)", prefixLen, ip.BitLen())
	}
	prefix = netip.PrefixFrom(ip, int(prefixLen))
	if prefix != prefix.Masked() {
		return 0, netip.Prefix{}, errors.New("lower bits not covered by prefix length are not all zero")
	}
	return requestID, prefix, nil
}

type routeAdvertisementCapsule struct {
	IPAddressRanges []IPRoute
}

// IPRoute represents an IP Address Range advertised in a ROUTE_ADVERTISEMENT capsule.
type IPRoute struct {
	StartIP netip.Addr
	EndIP   netip.Addr
	// IPProtocol is the Internet Protocol Number for traffic that can be sent to
	// this range. If 0, all protocols are allowed.
	IPProtocol uint8
}

func (r IPRoute) len() int { return 1 + r.StartIP.BitLen()/8 + r.EndIP.BitLen()/8 + 1 }

// Prefixes returns the prefixes that this IP address range covers.
func (r IPRoute) Prefixes() []netip.Prefix { return rangeToPrefixes(r.StartIP, r.EndIP) }

func parseRouteAdvertisementCapsule(r io.Reader) (*routeAdvertisementCapsule, error) {
	var ranges []IPRoute
	for {
		ipRange, err := parseIPAddressRange(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		ranges = append(ranges, ipRange)
	}
	return &routeAdvertisementCapsule{IPAddressRanges: ranges}, nil
}

func (c *routeAdvertisementCapsule) append(b []byte) []byte {
	var totalLen int
	for _, ipRange := range c.IPAddressRanges {
		totalLen += ipRange.len()
	}

	b = quicvarint.Append(b, uint64(capsuleTypeRouteAdvertisement))
	b = quicvarint.Append(b, uint64(totalLen))

	for _, ipRange := range c.IPAddressRanges {
		if ipRange.StartIP.Is4() {
			b = append(b, 4)
		} else {
			b = append(b, 6)
		}
		b = append(b, ipRange.StartIP.AsSlice()...)
		b = append(b, ipRange.EndIP.AsSlice()...)
		b = append(b, ipRange.IPProtocol)
	}
	return b
}

func parseIPAddressRange(r io.Reader) (IPRoute, error) {
	var ipVer uint8
	if err := binary.Read(r, binary.LittleEndian, &ipVer); err != nil {
		return IPRoute{}, err
	}

	var startIP, endIP netip.Addr
	switch ipVer {
	case 4:
		var start, end [4]byte
		if _, err := io.ReadFull(r, start[:]); err != nil {
			return IPRoute{}, err
		}
		if _, err := io.ReadFull(r, end[:]); err != nil {
			return IPRoute{}, err
		}
		startIP = netip.AddrFrom4(start)
		endIP = netip.AddrFrom4(end)
	case 6:
		var start, end [16]byte
		if _, err := io.ReadFull(r, start[:]); err != nil {
			return IPRoute{}, err
		}
		if _, err := io.ReadFull(r, end[:]); err != nil {
			return IPRoute{}, err
		}
		startIP = netip.AddrFrom16(start)
		endIP = netip.AddrFrom16(end)
	default:
		return IPRoute{}, fmt.Errorf("invalid IP version: %d", ipVer)
	}

	if startIP.Compare(endIP) > 0 {
		return IPRoute{}, errors.New("start IP is greater than end IP")
	}

	var ipProtocol uint8
	if err := binary.Read(r, binary.LittleEndian, &ipProtocol); err != nil {
		return IPRoute{}, err
	}
	return IPRoute{StartIP: startIP, EndIP: endIP, IPProtocol: ipProtocol}, nil
}
