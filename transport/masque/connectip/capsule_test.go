package connectip

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/quic-go/quicvarint"
)

func TestRouteAdvertisementRoundTrip(t *testing.T) {
	t.Parallel()
	orig := &routeAdvertisementCapsule{IPAddressRanges: []IPRoute{
		{
			StartIP:    netip.AddrFrom4([4]byte{}),
			EndIP:      netip.AddrFrom4([4]byte{255, 255, 255, 255}),
			IPProtocol: 0,
		},
	}}

	encoded := orig.append(nil)

	r := quicvarint.NewReader(bytes.NewReader(encoded))
	typ, body, err := http3.ParseCapsule(r)
	if err != nil {
		t.Fatal(err)
	}
	if typ != capsuleTypeRouteAdvertisement {
		t.Fatalf("wrong capsule type: %d", typ)
	}
	parsed, err := parseRouteAdvertisementCapsule(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.IPAddressRanges) != 1 {
		t.Fatalf("expected 1 range, got %d", len(parsed.IPAddressRanges))
	}
	got := parsed.IPAddressRanges[0]
	if got.StartIP != orig.IPAddressRanges[0].StartIP || got.EndIP != orig.IPAddressRanges[0].EndIP {
		t.Fatalf("range mismatch: %+v", got)
	}
}

func TestAddressAssignRoundTrip(t *testing.T) {
	t.Parallel()
	orig := &addressAssignCapsule{AssignedAddresses: []AssignedAddress{
		{RequestID: 0, IPPrefix: netip.MustParsePrefix("172.16.0.2/32")},
		{RequestID: 1, IPPrefix: netip.MustParsePrefix("2606:4700::/128")},
	}}
	encoded := orig.append(nil)

	r := quicvarint.NewReader(bytes.NewReader(encoded))
	typ, body, err := http3.ParseCapsule(r)
	if err != nil {
		t.Fatal(err)
	}
	if typ != capsuleTypeAddressAssign {
		t.Fatalf("wrong capsule type: %d", typ)
	}
	parsed, err := parseAddressAssignCapsule(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.AssignedAddresses) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(parsed.AssignedAddresses))
	}
	if parsed.AssignedAddresses[0].IPPrefix != orig.AssignedAddresses[0].IPPrefix {
		t.Fatalf("v4 prefix mismatch: %v", parsed.AssignedAddresses[0].IPPrefix)
	}
	if parsed.AssignedAddresses[1].IPPrefix != orig.AssignedAddresses[1].IPPrefix {
		t.Fatalf("v6 prefix mismatch: %v", parsed.AssignedAddresses[1].IPPrefix)
	}
}

func TestIPv4Checksum(t *testing.T) {
	t.Parallel()
	// A well-formed IPv4 header (from RFC examples): checksum field zeroed.
	header := [20]byte{
		0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00, 0xc0, 0xa8, 0x00, 0x01,
		0xc0, 0xa8, 0x00, 0xc7,
	}
	got := calculateIPv4Checksum(header)
	if got != 0xb861 {
		t.Fatalf("checksum = %#x, want 0xb861", got)
	}
}
