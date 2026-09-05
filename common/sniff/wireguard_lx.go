package sniff

// lx: SPEC 078 — WireGuard packet sniffer.
//
// A plain-WireGuard datagram is recognisable by its message type and size
// alone (RFC-less, but fixed by the protocol — see wireguard.com/protocol):
//
//	type 1  handshake initiation  148 B
//	type 2  handshake response     92 B
//	type 3  cookie reply           64 B
//	type 4  transport data        >= 32 B, (len-32) % 16 == 0
//
// The transport rule follows from the layout: 16-byte header (type, receiver
// index, counter) + ChaCha20-Poly1305 payload whose plaintext is padded to a
// multiple of 16 + 16-byte tag; a keepalive is the empty payload (32 B).
//
// The three reserved bytes after the type are NOT required to be zero:
// Cloudflare WARP writes a client id there, and the shape is unambiguous
// without them.
//
// Why this sniffer exists: the uTP sniffer (bittorrent.go, upstream) accepts
// any packet >= 20 B whose first byte is 0x01 (uTP v1, ST_DATA) and second
// byte is 0x00 (no extensions) — exactly the first two bytes of a WireGuard
// handshake initiation. Behind a sniffing router that mislabelled every plain
// WireGuard flow as bittorrent and routed it by the bittorrent rule. Placed
// before UTP in the default packet-sniffer order; also selectable by name
// ("wireguard") in the sniff rule action.
//
// AmneziaWG with non-default H1–H4 or S1–S4 padding deliberately does not
// match — hiding the shape is what those knobs are for.

import (
	"context"
	"os"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

const (
	wgMessageInitiation = 1
	wgMessageResponse   = 2
	wgMessageCookie     = 3
	wgMessageTransport  = 4

	wgSizeInitiation   = 148
	wgSizeResponse     = 92
	wgSizeCookie       = 64
	wgSizeTransportMin = 32 // 16-byte header + empty padded payload + 16-byte tag
)

// WireGuard detects a plain-WireGuard datagram by message type and size.
func WireGuard(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	if len(packet) < 4 {
		return os.ErrInvalid
	}
	switch packet[0] {
	case wgMessageInitiation:
		if len(packet) != wgSizeInitiation {
			return os.ErrInvalid
		}
	case wgMessageResponse:
		if len(packet) != wgSizeResponse {
			return os.ErrInvalid
		}
	case wgMessageCookie:
		if len(packet) != wgSizeCookie {
			return os.ErrInvalid
		}
	case wgMessageTransport:
		if len(packet) < wgSizeTransportMin || (len(packet)-wgSizeTransportMin)%16 != 0 {
			return os.ErrInvalid
		}
	default:
		return os.ErrInvalid
	}
	metadata.Protocol = C.ProtocolWireGuard
	return nil
}
