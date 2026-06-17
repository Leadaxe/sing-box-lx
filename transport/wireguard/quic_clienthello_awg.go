//go:build with_awg

// TLS 1.3 ClientHello builder for the QUIC Initial masquerade (§146 §3.1).
//
// This emits a realistic browser-shaped ClientHello (~294 bytes), NOT a bare
// SNI-only stub. The realism is load-bearing for the DPI bypass: the decoy must
// look like a genuine QUIC client's first flight (non-empty cipher_suites,
// key_share, ALPN "h3", quic_transport_params, supported_versions TLS1.3) so
// that when a DPI does reassemble or partially parse it, it classifies the flow
// as "QUIC to a CDN" rather than something to fingerprint. The exact extension
// set/order is not byte-critical (§146 §3.1) — what matters is that the required
// extensions are present and the total length lands near the etalon so the
// fragment cutpoints stay valid.
//
// We do not imitate a specific JA3/JA4 fingerprint (§146 §7): the bypass works
// on CRYPTO-frame fragmentation, not on TLS fingerprint, so a plausible generic
// ClientHello suffices.
package wireguard

import (
	"encoding/binary"

	E "github.com/sagernet/sing/common/exceptions"
)

// quicCHTargetLen is the target ClientHello length (handshake header included).
// The etalon is 294 bytes; we pad with a padding extension to hit this so the
// etalonCutpoints (last = 290) always yield non-empty fragments. A long SNI can
// push the CH past this, which is fine (see buildClientHello); a short SNI is
// padded up to it.
const quicCHTargetLen = 294

// quicCHMinLen is the minimum ClientHello length that still fragments at the
// etalon cutpoints (last cutpoint 290 ⇒ the final fragment needs ≥1 byte).
const quicCHMinLen = 291

// appendVec16 appends a 16-bit-length-prefixed vector: u16(len(body)) ‖ body.
func appendVec16(dst, body []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(body)))
	return append(dst, body...)
}

// appendExtension appends a TLS extension: u16(type) ‖ u16(len(data)) ‖ data.
func appendExtension(dst []byte, extType uint16, data []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, extType)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(data)))
	return append(dst, data...)
}

// buildClientHello assembles a TLS 1.3 ClientHello for the given SNI, using the
// supplied 32-byte TLS random and 32-byte x25519 public key. browser is accepted
// for API symmetry but does not change the structure (no JA3 imitation, §146 §7).
//
// Layout (RFC 8446 §4.1.2, wrapped as a handshake message):
//
//	handshake_type = 0x01 (ClientHello)
//	length         = u24
//	legacy_version = 0x0303
//	random         = 32 bytes
//	session_id     = empty (len 0)
//	cipher_suites  = [0x1301] (TLS_AES_128_GCM_SHA256) — non-empty (I-required)
//	compression    = null (0x00)
//	extensions     = supported_groups, signature_algorithms, server_name(SNI),
//	                 key_share(x25519), quic_transport_params, GREASE,
//	                 psk_key_exchange_modes, ALPN(h3), compress_certificate,
//	                 supported_versions(TLS1.3), padding(to target length)
func buildClientHello(sni string, tlsRandom [32]byte, x25519Pub []byte, browser string) ([]byte, error) {
	_ = browser // structure is browser-independent (§146 §7)
	if sni == "" {
		return nil, E.New("amneziawg: ip=quic requires a non-empty id (SNI) for the ClientHello")
	}
	if len(x25519Pub) != 32 {
		return nil, E.New("amneziawg: x25519 public key must be 32 bytes")
	}

	// --- extensions -------------------------------------------------------
	var exts []byte

	// supported_groups (0x000a): x25519 (0x001d). NamedGroupList is a u16-vec.
	exts = appendExtension(exts, 0x000a, appendVec16(nil, []byte{0x00, 0x1d}))

	// signature_algorithms (0x000d): a small plausible list (u16-vec of u16s).
	sigAlgs := []byte{
		0x04, 0x03, // ecdsa_secp256r1_sha256
		0x08, 0x04, // rsa_pss_rsae_sha256
		0x04, 0x01, // rsa_pkcs1_sha256
		0x08, 0x05, // rsa_pss_rsae_sha384
		0x05, 0x01, // rsa_pkcs1_sha384
		0x08, 0x06, // rsa_pss_rsae_sha512
		0x06, 0x01, // rsa_pkcs1_sha512
		0x02, 0x01, // rsa_pkcs1_sha1
	}
	exts = appendExtension(exts, 0x000d, appendVec16(nil, sigAlgs))

	// server_name (0x0000): ServerNameList = u16-vec of { name_type(0) ‖ u16-vec(host) }.
	var sniEntry []byte
	sniEntry = append(sniEntry, 0x00) // name_type = host_name
	sniEntry = appendVec16(sniEntry, []byte(sni))
	exts = appendExtension(exts, 0x0000, appendVec16(nil, sniEntry))

	// key_share (0x0033): KeyShareClientHello = u16-vec of { group(u16) ‖ u16-vec(key) }.
	var ks []byte
	ks = binary.BigEndian.AppendUint16(ks, 0x001d) // x25519
	ks = appendVec16(ks, x25519Pub)
	exts = appendExtension(exts, 0x0033, appendVec16(nil, ks))

	// quic_transport_params (0x0039): opaque plausible filler. The decoy never
	// completes a handshake, and DPI does not parse these; the byte content is
	// not validated, only the presence + plausible length matters (§146 §6.2).
	// Each param is varint(id) ‖ varint(len) ‖ value; we emit a few common ones.
	qtp := buildQUICTransportParams()
	exts = appendExtension(exts, 0x0039, qtp)

	// GREASE (0x0a0a, an RFC 8701 GREASE extension value): short opaque body.
	// Real clients send GREASE; a server/DPI that recognises GREASE codepoints
	// ignores it, which is exactly the "looks like a real client" effect we want.
	exts = appendExtension(exts, 0x0a0a, []byte{0x00, 0x00, 0x00})

	// psk_key_exchange_modes (0x002d): u8-vec { psk_dhe_ke(0x01) }.
	exts = appendExtension(exts, 0x002d, []byte{0x01, 0x01})

	// ALPN (0x0010): ProtocolNameList = u16-vec of { u8-vec("h3") }.
	var alpn []byte
	alpn = append(alpn, 0x02, 'h', '3') // u8-len(2) ‖ "h3"
	exts = appendExtension(exts, 0x0010, appendVec16(nil, alpn))

	// compress_certificate (0x001b): u8-vec of u16 algorithms { brotli(0x0002) }.
	exts = appendExtension(exts, 0x001b, []byte{0x02, 0x00, 0x02})

	// supported_versions (0x002b): u8-vec of u16 { TLS1.3(0x0304) }.
	exts = appendExtension(exts, 0x002b, []byte{0x02, 0x03, 0x04})

	// --- ClientHello body (before the handshake header) -------------------
	var body []byte
	body = append(body, 0x03, 0x03)              // legacy_version TLS1.2
	body = append(body, tlsRandom[:]...)         // 32-byte random
	body = append(body, 0x00)                    // session_id length 0
	body = appendVec16(body, []byte{0x13, 0x01}) // cipher_suites: TLS_AES_128_GCM_SHA256
	body = append(body, 0x01, 0x00)              // compression_methods: len 1, null

	// padding (0x0015): zero-filled, sized so the total handshake message reaches
	// AT LEAST quicCHTargetLen. The SNI is interpolated 1:1 into server_name, so a
	// long (but valid, ≤253-byte) domain can already exceed the etalon length
	// before any padding — in that case we add zero padding and let the CH be
	// naturally longer. The fragment plan (etalonCutpoints, last=290) only needs
	// len(ch) > 290, and a longer CH keeps I1–I4 (the final fragment just runs
	// longer); the device-proven geometry is preserved for typical short domains.
	// Compute the slack accounting for the extensions block length prefix (u16)
	// and the padding extension's own 4-byte header.
	const handshakeHdr = 4 // type(1) + length(3)
	const extsLenPrefix = 2
	const padExtHdr = 4
	current := handshakeHdr + len(body) + extsLenPrefix + len(exts)
	pad := quicCHTargetLen - current - padExtHdr
	if pad < 0 {
		pad = 0 // already at/over target — a long SNI; emit a 0-length padding ext
	}
	exts = appendExtension(exts, 0x0015, make([]byte, pad))

	// extensions block (u16-length-prefixed) appended to the body.
	body = appendVec16(body, exts)

	// --- wrap as a handshake message --------------------------------------
	out := make([]byte, 0, handshakeHdr+len(body))
	out = append(out, 0x01) // ClientHello
	out = append(out, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	out = append(out, body...)

	// Must be at least the etalon length so the offset cutpoints (last 290) yield
	// non-empty fragments; for short SNIs padding pins it exactly to the target.
	if len(out) < quicCHMinLen {
		return nil, E.New("amneziawg: ClientHello assembled shorter than the minimum fragmentable length")
	}
	return out, nil
}

// buildQUICTransportParams returns an opaque-but-plausible quic_transport_params
// extension body (§146 §6.2): a handful of common parameters with sane values.
// Not semantically validated by the decoy; present for realism only.
func buildQUICTransportParams() []byte {
	var p []byte
	appendParam := func(id uint64, value []byte) {
		p = appendQUICVarint(p, id)
		p = appendQUICVarint(p, uint64(len(value)))
		p = append(p, value...)
	}
	// 0x01 max_idle_timeout = 30000 ms (varint).
	appendParam(0x01, appendQUICVarint(nil, 30000))
	// 0x04 initial_max_data = 0x00c00000.
	appendParam(0x04, appendQUICVarint(nil, 0x00c00000))
	// 0x05 initial_max_stream_data_bidi_local = 0x00100000.
	appendParam(0x05, appendQUICVarint(nil, 0x00100000))
	// 0x06 initial_max_stream_data_bidi_remote = 0x00100000.
	appendParam(0x06, appendQUICVarint(nil, 0x00100000))
	// 0x07 initial_max_stream_data_uni = 0x00100000.
	appendParam(0x07, appendQUICVarint(nil, 0x00100000))
	// 0x08 initial_max_streams_bidi = 100.
	appendParam(0x08, appendQUICVarint(nil, 100))
	// 0x09 initial_max_streams_uni = 100.
	appendParam(0x09, appendQUICVarint(nil, 100))
	// 0x0e active_connection_id_limit = 8.
	appendParam(0x0e, appendQUICVarint(nil, 8))
	// 0x03 max_udp_payload_size = 1472.
	appendParam(0x03, appendQUICVarint(nil, 1472))
	return p
}
