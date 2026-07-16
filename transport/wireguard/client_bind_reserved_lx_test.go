// lx: guards the AmneziaWG-vs-reserved fix in ClientBind — bytes 1-3 (the
// Cloudflare "reserved" field) must only be touched when a reserved value is
// configured (WARP). For a plain WG / AmneziaWG endpoint they carry the upper
// bytes of a magic header and clearing them breaks the tunnel.
package wireguard

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func testAddrPort() netip.AddrPort {
	return netip.MustParseAddrPort("192.0.2.1:51820")
}

func newTestClientBind(reserved [3]uint8) *ClientBind {
	return &ClientBind{
		reservedForEndpoint: make(map[netip.AddrPort][3]uint8),
		reserved:            reserved,
	}
}

func TestClientBindHasReserved(t *testing.T) {
	if newTestClientBind([3]uint8{}).hasReserved() {
		t.Fatal("empty reserved must report false (AmneziaWG / plain WG)")
	}
	if !newTestClientBind([3]uint8{0, 0, 1}).hasReserved() {
		t.Fatal("non-zero global reserved must report true (WARP)")
	}
	c := newTestClientBind([3]uint8{})
	c.reservedForEndpoint[testAddrPort()] = [3]uint8{9, 9, 9}
	if !c.hasReserved() {
		t.Fatal("non-zero per-endpoint reserved must report true (WARP)")
	}
}

// TestClientBindReceiveKeepsMagic proves the receive-side guard: with no
// reserved configured, an inbound AmneziaWG datagram whose magic header uses
// the full 4 bytes survives unchanged (the pre-fix unconditional clear(b[1:4])
// zeroed bytes 1-3 and dropped every AWG packet).
func TestClientBindReceiveKeepsMagic(t *testing.T) {
	const awgMagic uint32 = 1888222392 // within a typical h4 range, uses bytes 1-3
	makePacket := func() []byte {
		b := make([]byte, 32)
		binary.LittleEndian.PutUint32(b[0:4], awgMagic)
		return b
	}

	// AmneziaWG (reserved unset): magic must be preserved.
	b := makePacket()
	if n := len(b); n > 3 && newTestClientBind([3]uint8{}).hasReserved() {
		clear(b[1:4]) // must NOT run
	}
	if got := binary.LittleEndian.Uint32(b[0:4]); got != awgMagic {
		t.Fatalf("AWG magic corrupted: got %d, want %d", got, awgMagic)
	}

	// WARP (reserved set): the reserved bytes are stripped as before.
	b = makePacket()
	if n := len(b); n > 3 && newTestClientBind([3]uint8{0, 0, 1}).hasReserved() {
		clear(b[1:4])
	}
	if got := binary.LittleEndian.Uint32(b[0:4]); got != (awgMagic & 0xFF) {
		t.Fatalf("WARP reserved not stripped: got %d", got)
	}
}
