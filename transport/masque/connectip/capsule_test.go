package connectip

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/sagernet/quic-go/http3"
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

	// lx: quic-go v0.61 replaced ParseCapsule with the stateful CapsuleParser.
	typ, body, err := http3.NewCapsuleParser(bytes.NewReader(encoded)).Next()
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

	// lx: quic-go v0.61 replaced ParseCapsule with the stateful CapsuleParser.
	typ, body, err := http3.NewCapsuleParser(bytes.NewReader(encoded)).Next()
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
	got := calculateIPv4Checksum(header[:])
	if got != 0xb861 {
		t.Fatalf("checksum = %#x, want 0xb861", got)
	}
}

// TestIPv4ChecksumWithOptions covers a header carrying an IPv4 option (IHL=6, so
// 24 bytes): the checksum must fold in the option word, not just the first 20
// bytes (SPEC 022 #6). Expected value is the ones-complement sum over all 24
// bytes with the checksum field zeroed.
func TestIPv4ChecksumWithOptions(t *testing.T) {
	t.Parallel()
	header := []byte{
		0x46, 0x00, 0x00, 0x77, 0x00, 0x00, 0x40, 0x00, // IHL=6 (0x46)
		0x40, 0x11, 0x00, 0x00, 0xc0, 0xa8, 0x00, 0x01,
		0xc0, 0xa8, 0x00, 0xc7,
		0x94, 0x04, 0x00, 0x00, // option: Router Alert (type 0x94, len 4)
	}
	// Independent reference sum over the 24 bytes, checksum field (10-11) zeroed.
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		if i == 10 {
			continue
		}
		sum += uint32(header[i])<<8 | uint32(header[i+1])
	}
	for sum>>16 > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	want := ^uint16(sum)
	if got := calculateIPv4Checksum(header); got != want {
		t.Fatalf("checksum over IHL=6 header = %#x, want %#x", got, want)
	}
	// The 20-byte-only sum would differ, proving the option word is now covered.
	if want == calculateIPv4Checksum(header[:20]) {
		t.Fatal("option word did not change the checksum — coverage not exercised")
	}
}
