//go:build with_awg

// QUIC 1-RTT short-header masquerade generator.
//
// Ported structure from the open-source WireSock reference:
//
//	https://github.com/wiresock/amneziawg-install
//	amneziawg-proxy/src/transform.rs::apply_quic_padding_short (MIT License,
//	Copyright (c) WireSock)
//
// WireSock deliberately imitates a QUIC **1-RTT short header**, NOT an Initial
// packet with a ClientHello. Their stated reasons (from transform.rs):
//   - A QUIC Initial requires a datagram of at least 1200 bytes (RFC 9000
//     §14.1), which a small decoy cannot meet.
//   - A 1-RTT short header has no version field and no length field, so the
//     bytes after the first are indistinguishable from encrypted 1-RTT
//     ciphertext — the dominant and least-conspicuous QUIC packet type.
//
// Consequence for the threat model: a short header does NOT expose an SNI. This
// decoy does not advertise the configured domain to a censor that decrypts QUIC
// Initials (there is nothing to decrypt). The domain (Id) is validated and used
// by the dns/sip profiles; for quic it only gates configuration consistency.
// This is intentional and honest — we do not claim a "byte-perfect" Initial nor
// a TLS fingerprint here. See SPECS/009-* for the rationale and the rejected
// mini_quic_generator Initial approach.
package wireguard

// masqueQUICShortHeaderCPS builds a QUIC 1-RTT short-header decoy:
//
//	byte 0: 0x40 | (spin<<5) | (key_phase<<2) | pn_len  (form=0, fixed=1,
//	        reserved bits cleared — RFC 9000 §17.3.1)
//	bytes 1..: a Destination Connection ID followed by pseudo-random bytes
//	        simulating the encrypted 1-RTT payload.
//
// The first byte is static (a CPS <b> tag cannot carry per-bit randomness), so
// spin/key_phase/pn_len are fixed at generation time. WireSock randomises them
// per packet from a payload seed; we have no payload, so we derive them from the
// browser hint (Ib) purely so different browsers produce a slightly different —
// but equally valid — first byte. This is the ONLY effect of Ib (see
// normalizeMasqueBrowser); it is cosmetic, as these bits are per-packet random
// in real QUIC and carry no fingerprint.
//
// The connection ID and payload are <r N> (fresh cryptographic randomness each
// packet), which is exactly the correct appearance for 1-RTT ciphertext.
func masqueQUICShortHeaderCPS(domain, browser string) string {
	_ = domain // not exposed by a short header (no SNI); see file header.

	first := quicFirstByte(browser)

	const (
		dcidLen    = 8  // Destination Connection ID length (typical)
		payloadLen = 32 // simulated encrypted 1-RTT payload
	)

	var b cpsBuilder
	b.addBytes([]byte{first})
	b.addRand(dcidLen + payloadLen)
	return b.String()
}

// quicFirstByte computes the QUIC 1-RTT short-header first byte for a browser
// hint. form=0, fixed=1 (0x40) are mandatory; reserved bits (0x18) must be 0
// (RFC 9000 §17.3). spin (0x20), key_phase (0x04) and pn_len (0x03) are free
// bits that are per-packet random in real QUIC; we pick a fixed, valid
// combination per browser so the value is deterministic but plausible.
//
// All returned bytes satisfy (b & 0xC0) == 0x40 and (b & 0x18) == 0.
func quicFirstByte(browser string) byte {
	const base = 0x40 // form=0, fixed=1, reserved=00
	switch browser {
	case masqueBrowserChrome:
		// spin=1, key_phase=0, pn_len=00 -> packet number length 1.
		return base | 0x20 | 0x00 | 0x00
	case masqueBrowserFirefox:
		// spin=0, key_phase=1, pn_len=01 -> packet number length 2.
		return base | 0x00 | 0x04 | 0x01
	case masqueBrowserCurl:
		// spin=1, key_phase=1, pn_len=11 -> packet number length 4.
		return base | 0x20 | 0x04 | 0x03
	default:
		// No browser hint: spin=0, key_phase=0, pn_len=00.
		return base
	}
}
