//go:build with_awg

// RFC 9001 §5 Initial-encryption primitives for the QUIC masquerade.
//
// These are mirrored BYTE-FOR-BYTE from the project's own QUIC TLS helpers in
// common/sniff/internal/qtls/qtls.go. We cannot import that package directly —
// it lives under common/sniff/internal/, so Go only permits imports from code
// rooted at common/sniff/. Rather than relocate upstream's package, we keep a
// local copy of just the three primitives the generator needs. Because the
// encoding is identical to qtls (and to RFC 9001), the keys we derive match what
// the sniffer in common/sniff/quic.go computes for the same DCID — which is what
// lets that sniffer (and the test reverse-parser) decrypt our output.
//
// These are not "custom crypto": HKDF-Expand-Label is RFC 8446 §7.1 verbatim,
// the salt is the RFC 9001 §5.2 QUIC v1 constant, and the AEAD is AES-128-GCM
// with the TLS-1.3 XORed-nonce construction (RFC 8446 §5.3 / RFC 9001 §5.3).
package wireguard

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"

	"golang.org/x/crypto/hkdf"
)

// quicVersion1 is the QUIC v1 version number (RFC 9000 §15).
const quicVersion1 uint32 = 0x1

// quicSaltV1 is the QUIC v1 Initial salt (RFC 9001 §5.2). Identical to
// qtls.SaltV1.
var quicSaltV1 = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

// quicHKDFExpandLabel is HKDF-Expand-Label (RFC 8446 §7.1) with the "tls13 "
// label prefix. Identical to qtls.HKDFExpandLabel.
func quicHKDFExpandLabel(hash crypto.Hash, secret, context []byte, label string, length int) []byte {
	b := make([]byte, 3, 3+6+len(label)+1+len(context))
	binary.BigEndian.PutUint16(b, uint16(length))
	b[2] = uint8(6 + len(label))
	b = append(b, []byte("tls13 ")...)
	b = append(b, []byte(label)...)
	b = b[:3+6+len(label)+1]
	b[3+6+len(label)] = uint8(len(context))
	b = append(b, context...)
	out := make([]byte, length)
	n, err := hkdf.Expand(hash.New, secret, b).Read(out)
	if err != nil || n != length {
		panic("quic: HKDF-Expand-Label invocation failed unexpectedly")
	}
	return out
}

// quicAEADAESGCMTLS13 builds the TLS-1.3 AEAD (AES-128-GCM with a XORed nonce).
// The 8-byte nonce passed to Seal/Open is XORed onto the low 8 bytes of the
// 12-byte iv (nonceMask). Identical to qtls.AEADAESGCMTLS13.
func quicAEADAESGCMTLS13(key, nonceMask []byte) cipher.AEAD {
	if len(nonceMask) != 12 {
		panic("tls: internal error: wrong nonce length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	ret := &quicXorNonceAEAD{aead: aead}
	copy(ret.nonceMask[:], nonceMask)
	return ret
}

type quicXorNonceAEAD struct {
	nonceMask [12]byte
	aead      cipher.AEAD
}

func (f *quicXorNonceAEAD) NonceSize() int { return 8 } // 64-bit sequence number
func (f *quicXorNonceAEAD) Overhead() int  { return f.aead.Overhead() }

func (f *quicXorNonceAEAD) Seal(out, nonce, plaintext, additionalData []byte) []byte {
	for i, b := range nonce {
		f.nonceMask[4+i] ^= b
	}
	result := f.aead.Seal(out, f.nonceMask[:], plaintext, additionalData)
	for i, b := range nonce {
		f.nonceMask[4+i] ^= b
	}
	return result
}

func (f *quicXorNonceAEAD) Open(out, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	for i, b := range nonce {
		f.nonceMask[4+i] ^= b
	}
	result, err := f.aead.Open(out, f.nonceMask[:], ciphertext, additionalData)
	for i, b := range nonce {
		f.nonceMask[4+i] ^= b
	}
	return result, err
}
