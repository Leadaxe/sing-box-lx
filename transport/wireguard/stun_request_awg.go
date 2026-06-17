//go:build with_awg

// STUN Binding Request masquerade generator — a WebRTC/ICE connectivity-check
// packet (009 masquerade, ip=stun).
//
// This SUPERSEDES the earlier Binding Success Response decoy. A Success Response
// (0x0101) is by definition the server's reply echoing a prior request; sending
// one as the client's FIRST, unsolicited packet is a wrong-direction anomaly. A
// Binding REQUEST (0x0001) is what an ICE agent legitimately sends first, so it
// is the RFC-consistent shape for a client-initiated decoy.
//
// Device status. On the LTE/WARP DPI this feature targets this did NOT pass
// device testing — neither this nor a minimal Binding Request — which indicates
// that DPI blocks STUN toward a datacenter Cloudflare IP as a protocol class,
// not on packet quality. We ship the strongest possible STUN shape anyway: it
// may pass on a different provider whose DPI only checks "is this a well-formed
// STUN packet". QUIC (quic_initial_awg.go) remains the proven mechanism. See the
// 009 spec for the device-test record.
//
// The packet is a full WebRTC connectivity check: USERNAME, ICE-CONTROLLING,
// PRIORITY, SOFTWARE, MESSAGE-INTEGRITY (HMAC-SHA1, RFC 5389 §15.4) and
// FINGERPRINT (CRC-32, §15.5). MESSAGE-INTEGRITY is keyed with a random
// short-term credential we invent per call: an on-path DPI cannot verify the
// HMAC without the shared ICE password (which only the two ICE peers hold), so
// structural presence — not cryptographic validity against a real session — is
// what a DPI can check. Fresh transaction ID, ufrag, tie-breaker and integrity
// key per call ⇒ no cross-user signature.
package wireguard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"hash/crc32"
)

const (
	stunTypeBindingRequest = 0x0001
	stunAttrUsername       = 0x0006
	stunAttrMsgIntegrity   = 0x0008
	stunAttrPriority       = 0x0024
	stunAttrSoftware       = 0x8022
	stunAttrIceControlling = 0x802a
	stunAttrFingerprint    = 0x8028
	stunFingerprintXOR     = 0x5354554e
)

// stunSoftwareProduct is a plausible product string (a real client sends a
// product name here, not random bytes). Mirrors a common libwebrtc value.
const stunSoftwareProduct = "libwebrtc"

// appendSTUNAttr appends a STUN attribute (type, length, value) padded to a
// 4-byte boundary (RFC 5389 §15).
func appendSTUNAttr(dst []byte, typ uint16, val []byte) []byte {
	var h [4]byte
	binary.BigEndian.PutUint16(h[0:], typ)
	binary.BigEndian.PutUint16(h[2:], uint16(len(val)))
	dst = append(dst, h[:]...)
	dst = append(dst, val...)
	for len(dst)%4 != 0 {
		dst = append(dst, 0x00)
	}
	return dst
}

// buildSTUNBindingRequest assembles a full WebRTC-style STUN Binding Request with
// fresh per-call entropy. The header message-length is computed in two stages so
// that MESSAGE-INTEGRITY covers the message up to (and including) itself and
// FINGERPRINT covers the message up to (and including) itself, per RFC 5389.
func buildSTUNBindingRequest() ([]byte, error) {
	// Fresh entropy: transaction ID, ufrag pair, ICE tie-breaker, integrity key.
	txn := make([]byte, 12)
	if _, err := rand.Read(txn); err != nil {
		return nil, err
	}
	ufrag := make([]byte, 9) // "<4 hex>:<4 hex>" style username
	if _, err := rand.Read(ufrag); err != nil {
		return nil, err
	}
	tie := make([]byte, 8)
	if _, err := rand.Read(tie); err != nil {
		return nil, err
	}
	prio := make([]byte, 4)
	if _, err := rand.Read(prio); err != nil {
		return nil, err
	}
	integrityKey := make([]byte, 16)
	if _, err := rand.Read(integrityKey); err != nil {
		return nil, err
	}

	username := stunICEUsername(ufrag)

	// Attributes before MESSAGE-INTEGRITY.
	var attrs []byte
	attrs = appendSTUNAttr(attrs, stunAttrUsername, username)
	attrs = appendSTUNAttr(attrs, stunAttrIceControlling, tie)
	attrs = appendSTUNAttr(attrs, stunAttrPriority, prio)
	attrs = appendSTUNAttr(attrs, stunAttrSoftware, []byte(stunSoftwareProduct))

	// Header. message-length must count up to & including MESSAGE-INTEGRITY (24 B
	// total: 4 attr header + 20 HMAC) when computing the HMAC.
	const miAttrTotal = 24
	header := make([]byte, 20)
	binary.BigEndian.PutUint16(header[0:], stunTypeBindingRequest)
	binary.BigEndian.PutUint32(header[4:], stunMagicCookie)
	copy(header[8:], txn)

	binary.BigEndian.PutUint16(header[2:], uint16(len(attrs)+miAttrTotal))
	preMI := append(append([]byte{}, header...), attrs...)
	mac := hmac.New(sha1.New, integrityKey)
	mac.Write(preMI)
	mi := mac.Sum(nil) // 20 bytes
	withMI := appendSTUNAttr(preMI, stunAttrMsgIntegrity, mi)

	// FINGERPRINT: message-length now counts up to & including FINGERPRINT (+8),
	// CRC-32 over everything before FINGERPRINT, XORed with the magic constant.
	const fpAttrTotal = 8
	binary.BigEndian.PutUint16(withMI[2:], uint16(len(attrs)+miAttrTotal+fpAttrTotal))
	crc := crc32.ChecksumIEEE(withMI) ^ stunFingerprintXOR
	var fp [4]byte
	binary.BigEndian.PutUint32(fp[:], crc)
	pkt := appendSTUNAttr(withMI, stunAttrFingerprint, fp[:])

	return pkt, nil
}

// stunICEUsername renders a libwebrtc-style "ufrag:ufrag" username from random
// bytes, using only LDH-safe lowercase hex so the value is printable ASCII.
func stunICEUsername(seed []byte) []byte {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 9)
	for i := 0; i < 4; i++ {
		out = append(out, hexdigits[seed[i]%16])
	}
	out = append(out, ':')
	for i := 4; i < 8; i++ {
		out = append(out, hexdigits[seed[i]%16])
	}
	return out
}

// masqueSTUNRequestCPS builds the WebRTC Binding Request decoy and emits it as a
// single static-bytes CPS tag (uniqueness baked in per call — the FINGERPRINT
// CRC and MESSAGE-INTEGRITY HMAC depend on the random fields, so it cannot use
// <r> randomness).
func masqueSTUNRequestCPS() (string, error) {
	pkt, err := buildSTUNBindingRequest()
	if err != nil {
		return "", err
	}
	var b cpsBuilder
	b.addBytes(pkt)
	return b.String(), nil
}
