package sniff

// lx: SPEC 079 — Tailscale disco sniffer.
//
// Tailscale's NAT-traversal "disco" pings share the UDP socket with its
// WireGuard traffic and are the first thing a client sends to a peer:
//
//	"TS💬" (6 bytes) | sender disco public key [32] | nonce [24] | NaCl box (≥ 16)
//
// The tunnel itself is WireGuard and is covered by the wireguard sniffer.

import (
	"bytes"
	"context"
	"os"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

var tailscaleDiscoMagic = []byte("TS\xf0\x9f\x92\xac")

const tailscaleDiscoMinLen = 6 + 32 + 24 + 16

// TailscaleDisco detects a Tailscale disco packet.
func TailscaleDisco(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	if len(packet) < tailscaleDiscoMinLen || !bytes.HasPrefix(packet, tailscaleDiscoMagic) {
		return os.ErrInvalid
	}
	metadata.Protocol = C.ProtocolTailscale
	return nil
}
