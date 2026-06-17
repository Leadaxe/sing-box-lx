//go:build with_awg

// QUIC Initial masquerade generator — out-of-order fragmented ClientHello.
//
// This SUPERSEDES the earlier 1-RTT short-header decoy (the deleted
// masqueQUICShortHeaderCPS / quicFirstByte). That decoy was structurally valid
// QUIC but EMPIRICALLY BLOCKED by a real LTE-operator DPI: every short-header
// variant timed out, while a full QUIC Initial whose ClientHello is split into
// several out-of-order CRYPTO frames passed reliably (device-proven A/B, see
// LxBox docs/spec/tasks/146-warp-quic-initial-fragmented-i1.md §2).
//
// WHY OUT-OF-ORDER WORKS (mechanism, §146 §1). A real QUIC server reassembles
// CRYPTO frames by their offset before TLS parsing; a line-rate DPI does not
// keep a reassembly buffer — it grabs the FIRST CRYPTO frame, assumes it starts
// at offset 0, and parses TLS from there. When the first wire frame has
// offset≠0 (the middle of the ClientHello), the DPI parses garbage, the TLS
// record lengths do not add up, and it fails OPEN (Chrome legitimately
// fragments large ClientHellos, so fail-closed would break real QUIC). The real
// WARP server reorders the frames and the handshake would proceed; the DPI does
// not. The i1 is a standalone decoy (src=nil, sent before the WG handshake — see
// amneziawg-go send.go); it does not need to complete any TLS handshake, only to
// make the first packet of the flow look like a legitimate QUIC start to a CDN.
//
// This deliberately reverses the short-header file's old rationale (a decoy
// "cannot meet" the ≥1200-byte Initial minimum, and a short header "hides
// better"): the device evidence shows the opposite on real DPI, and a padded
// ≥1200-byte Initial is exactly what RFC 9000 §14.1 mandates anyway. Do NOT
// "simplify" this back to a short header.
//
// Crypto is RFC 9001 §5 Initial encryption. The HKDF-Expand-Label, QUIC v1 salt
// and AES-128-GCM-with-XORed-nonce AEAD live in quic_crypto_awg.go, mirrored
// byte-for-byte from the project's QUIC sniffer helpers (common/sniff so the
// derived keys are identical). The packet structure is the exact inverse of the
// live QUIC sniffer in common/sniff/quic.go — which doubles as the reverse
// parser the tests use to verify our own output (decrypt, frame-walk,
// reassemble, SNI).
package wireguard

import (
	"crypto"
	"crypto/aes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/crypto/hkdf"
)

// ---------------------------------------------------------------------------
// RFC 9000 §16 variable-length integer ENCODER.
//
// qtls.ReadUvarint is decode-only; we need the encoder for CRYPTO offset/length
// and the Initial length field. Two-bit length prefix: 00→1 byte (<2^6),
// 01→2 bytes (<2^14), 10→4 bytes (<2^30), 11→8 bytes (<2^62).
// ---------------------------------------------------------------------------

func appendQUICVarint(dst []byte, v uint64) []byte {
	switch {
	case v < 1<<6:
		return append(dst, byte(v))
	case v < 1<<14:
		return append(dst, byte(v>>8)|0x40, byte(v))
	case v < 1<<30:
		return append(dst,
			byte(v>>24)|0x80, byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(dst,
			byte(v>>56)|0xc0, byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

// ---------------------------------------------------------------------------
// QUIC Initial geometry (etalon from §146 §3.2/§3.3).
// ---------------------------------------------------------------------------

const (
	quicInitialTotalLen = 1250 // total datagram size (padded ≥1200, RFC 9000 §14.1)
	quicInitialLenField = 1232 // varint "length" = pn_len + payload + tag (0x44d0)
	quicPacketNumberLen = 1    // packet number length in bytes (pn_len=1)
	quicAEADTagLen      = 16   // AES-128-GCM authentication tag
	quicDCIDLen         = 8    // Destination Connection ID length (fresh per call)
)

// ---------------------------------------------------------------------------
// CRYPTO fragment plan (wire order). The OUT-OF-ORDER wire layout is the whole
// DPI bypass: the first emitted CRYPTO frame is offset≠0, the offset-0 frame is
// emitted near the end, PING/PADDING are interleaved between CRYPTO frames.
//
// Invariants (§146 §3.2), enforced by buildInitialPayload + asserted by tests:
//   I1. first CRYPTO frame in wire order has offset≠0.
//   I2. the offset-0 CRYPTO frame is NOT first.
//   I3. PADDING runs and ≥1 PING between CRYPTO frames.
//   I4. union of CRYPTO frames by offset = contiguous ClientHello [0..N), no gap.
// ---------------------------------------------------------------------------

// frameKind tags an entry in the wire-order plan.
type frameKind int

const (
	frameCrypto frameKind = iota
	framePing
	framePadding
)

// planEntry is one frame in wire order. For frameCrypto, cryptoIdx selects which
// ClientHello slice (by offset boundary) to emit. For framePadding, padLen is
// the run length of zero bytes.
type planEntry struct {
	kind      frameKind
	cryptoIdx int  // index into the offset-sorted fragment list
	padLen    int  // PADDING run length (when padFlex is false)
	padFlex   bool // PADDING run sized at build time to absorb the remaining slack
}

// cryptoFragment is one contiguous slice of the ClientHello at a given offset.
type cryptoFragment struct {
	offset uint64
	data   []byte
}

// etalonCutpoints are the device-proven ClientHello offset boundaries (§3.2):
// fragment lengths 236, 30, 9, 8, 7, 4 → contiguous [0..294). planFragments
// slices the ClientHello here; a CH of any length ≥ the last cutpoint reassembles
// to the same boundaries (the final fragment just runs to len(ch)).
var etalonCutpoints = []uint64{0, 236, 266, 275, 283, 290}

// etalonWirePlan is the device-proven wire order (§3.2). cryptoIdx refers to the
// offset-sorted fragment index (0 = offset 0, 1 = offset 236, ... 5 = offset 290).
// First CRYPTO is fragment 1 (offset 236) ⇒ I1; fragment 0 (offset 0) is the
// 10th frame ⇒ I2; PING+PADDING interleaved ⇒ I3.
// One PADDING entry carries padFlex=true: its run length is computed at build
// time as whatever is left to reach the payload target. This keeps the payload
// pinned to the length field for ANY ClientHello size (a long SNI grows the CH
// and shrinks this run), instead of relying on the fixed runs summing exactly —
// which would overflow the moment the CH exceeds the etalon 294 bytes.
var etalonWirePlan = []planEntry{
	{kind: frameCrypto, cryptoIdx: 1},   // 1: CRYPTO off=236 len=30  (offset≠0 first — I1)
	{kind: framePadding, padLen: 176},   // 2: PADDING run
	{kind: framePing},                   // 3: PING
	{kind: framePadding, padFlex: true}, // 4: PADDING run (flex — absorbs the slack)
	{kind: frameCrypto, cryptoIdx: 2},   // 5: CRYPTO off=266 len=9
	{kind: framePing},                   // 6: PING
	{kind: frameCrypto, cryptoIdx: 4},   // 7: CRYPTO off=283 len=7
	{kind: frameCrypto, cryptoIdx: 5},   // 8: CRYPTO off=290 len=4 (CH tail)
	{kind: framePadding, padLen: 72},    // 9: PADDING run
	{kind: frameCrypto, cryptoIdx: 0},   // 10: CRYPTO off=0 len=236 (start — near end, I2)
	{kind: frameCrypto, cryptoIdx: 3},   // 11: CRYPTO off=275 len=8
}

// planFragments slices the ClientHello at etalonCutpoints into offset-indexed
// fragments. fragments[i].offset == etalonCutpoints[i]; the final fragment runs
// to len(ch). Returns an error if the ClientHello is shorter than the last
// cutpoint (would yield an empty/negative fragment).
func planFragments(ch []byte) ([]cryptoFragment, error) {
	last := etalonCutpoints[len(etalonCutpoints)-1]
	if uint64(len(ch)) <= last {
		return nil, E.New("amneziawg: ClientHello too short to fragment at etalon cutpoints")
	}
	frags := make([]cryptoFragment, len(etalonCutpoints))
	for i, off := range etalonCutpoints {
		end := uint64(len(ch))
		if i+1 < len(etalonCutpoints) {
			end = etalonCutpoints[i+1]
		}
		frags[i] = cryptoFragment{offset: off, data: ch[off:end]}
	}
	return frags, nil
}

// appendCryptoFrame writes a CRYPTO frame: 0x06 ‖ varint(offset) ‖ varint(len) ‖ data.
func appendCryptoFrame(dst []byte, f cryptoFragment) []byte {
	dst = append(dst, 0x06)
	dst = appendQUICVarint(dst, f.offset)
	dst = appendQUICVarint(dst, uint64(len(f.data)))
	return append(dst, f.data...)
}

// planFixedLen returns the byte length the plan contributes EXCLUDING the single
// flex PADDING run, plus a bool for whether a flex entry exists. Used to size the
// flex run so the whole payload lands exactly on targetLen.
func planFixedLen(frags []cryptoFragment, plan []planEntry) (int, bool, error) {
	total := 0
	flex := false
	for _, e := range plan {
		switch e.kind {
		case frameCrypto:
			if e.cryptoIdx < 0 || e.cryptoIdx >= len(frags) {
				return 0, false, E.New("amneziawg: bad crypto fragment index in wire plan")
			}
			f := frags[e.cryptoIdx]
			total += 1 + varintLen(f.offset) + varintLen(uint64(len(f.data))) + len(f.data)
		case framePing:
			total++
		case framePadding:
			if e.padFlex {
				flex = true
				continue
			}
			total += e.padLen
		}
	}
	return total, flex, nil
}

// buildInitialPayload emits the frames in wire order per the plan, sizing the
// flex PADDING run so the payload is exactly targetLen. The plan's order is what
// produces the out-of-order CRYPTO layout (I1–I3); planFragments guarantees the
// offsets reassemble contiguously (I4).
func buildInitialPayload(frags []cryptoFragment, plan []planEntry, targetLen int) ([]byte, error) {
	fixed, hasFlex, err := planFixedLen(frags, plan)
	if err != nil {
		return nil, err
	}
	if !hasFlex {
		return nil, E.New("amneziawg: wire plan has no flex PADDING run to absorb slack")
	}
	flexLen := targetLen - fixed
	if flexLen < 0 {
		return nil, E.New("amneziawg: QUIC Initial frames overflow the length field (ClientHello too large)")
	}

	out := make([]byte, 0, targetLen)
	for _, e := range plan {
		switch e.kind {
		case frameCrypto:
			out = appendCryptoFrame(out, frags[e.cryptoIdx])
		case framePing:
			out = append(out, 0x01)
		case framePadding:
			n := e.padLen
			if e.padFlex {
				n = flexLen
			}
			for i := 0; i < n; i++ {
				out = append(out, 0x00)
			}
		}
	}
	if len(out) != targetLen {
		return nil, E.New("amneziawg: QUIC Initial payload did not land on the length field")
	}
	return out, nil
}

// varintLen returns how many bytes appendQUICVarint will emit for v.
func varintLen(v uint64) int {
	switch {
	case v < 1<<6:
		return 1
	case v < 1<<14:
		return 2
	case v < 1<<30:
		return 4
	default:
		return 8
	}
}

// ---------------------------------------------------------------------------
// RFC 9001 §5 Initial encryption. Mirror of common/sniff/quic.go (decrypt path),
// reusing qtls helpers. For pn_len=1 / packet number 0.
// ---------------------------------------------------------------------------

// deriveInitialKeys derives the client Initial key/iv/hp from the DCID
// (RFC 9001 §5.1/§5.2): HKDF-Extract(salt, dcid) → "client in" → quic key/iv/hp.
func deriveInitialKeys(dcid []byte) (key, iv, hp []byte) {
	initialSecret := hkdf.Extract(crypto.SHA256.New, dcid, quicSaltV1)
	clientSecret := quicHKDFExpandLabel(crypto.SHA256, initialSecret, []byte{}, "client in", crypto.SHA256.Size())
	key = quicHKDFExpandLabel(crypto.SHA256, clientSecret, []byte{}, "quic key", 16)
	iv = quicHKDFExpandLabel(crypto.SHA256, clientSecret, []byte{}, "quic iv", 12)
	hp = quicHKDFExpandLabel(crypto.SHA256, clientSecret, []byte{}, "quic hp", 16)
	return
}

// encryptInitial seals the payload and applies header protection in place.
// header is the unprotected long header up to and including the packet number;
// pnOffset is the byte index of the packet number within header. Returns the
// full wire packet: protected header ‖ ciphertext (incl. 16-byte tag).
//
// Header protection per RFC 9001 §5.4: sample the ciphertext at offset 4 from
// the start of the packet number field (so for pn_len=1, ct[3:19]), AES-ECB it
// with hp, then XOR the low 4 bits of the first byte with mask[0] and each
// packet-number byte with mask[1+i].
func encryptInitial(header, payload, key, iv, hp []byte, pnOffset int, pn uint64) ([]byte, error) {
	cipher := quicAEADAESGCMTLS13(key, iv)
	// nonce is 8 bytes (the sequence number); qtls XORs it onto iv internally.
	nonce := make([]byte, cipher.NonceSize())
	binary.BigEndian.PutUint64(nonce[cipher.NonceSize()-8:], pn)
	ciphertext := cipher.Seal(nil, nonce, payload, header)

	packet := make([]byte, 0, len(header)+len(ciphertext))
	packet = append(packet, header...)
	packet = append(packet, ciphertext...)

	// Sample starts 4 bytes after the packet-number field begins.
	sampleOffset := pnOffset + 4
	if sampleOffset+aes.BlockSize > len(packet) {
		return nil, E.New("amneziawg: QUIC Initial too short to sample for header protection")
	}
	block, err := aes.NewCipher(hp)
	if err != nil {
		return nil, err
	}
	mask := make([]byte, aes.BlockSize)
	block.Encrypt(mask, packet[sampleOffset:sampleOffset+aes.BlockSize])
	packet[0] ^= mask[0] & 0x0f // long header: protect low 4 bits of first byte
	for i := 0; i < quicPacketNumberLen; i++ {
		packet[pnOffset+i] ^= mask[1+i]
	}
	return packet, nil
}

// buildInitialPacket assembles a complete QUIC v1 Initial datagram carrying the
// fragmented ClientHello for sni. Fresh DCID, TLS random and ephemeral x25519
// key are generated per call, so every invocation yields a unique ciphertext.
func buildInitialPacket(sni, browser string) ([]byte, error) {
	dcid := make([]byte, quicDCIDLen)
	if _, err := rand.Read(dcid); err != nil {
		return nil, err
	}
	var tlsRandom [32]byte
	if _, err := rand.Read(tlsRandom[:]); err != nil {
		return nil, err
	}
	// Real ephemeral x25519 public key for the key_share extension (point-on-curve
	// valid, what a real client sends; the decoy never completes the ECDH).
	ecKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	clientHello, err := buildClientHello(sni, tlsRandom, ecKey.PublicKey().Bytes(), browser)
	if err != nil {
		return nil, err
	}

	frags, err := planFragments(clientHello)
	if err != nil {
		return nil, err
	}

	// The length field covers pn_len + plaintext payload + AEAD tag. So the
	// plaintext payload region is lenField - pn_len - tag.
	payloadLen := quicInitialLenField - quicPacketNumberLen - quicAEADTagLen
	payload, err := buildInitialPayload(frags, etalonWirePlan, payloadLen)
	if err != nil {
		return nil, err
	}

	// Unprotected long header: first byte 0xC0|(pn_len-1), version 1, DCID,
	// SCID len 0, token len 0, length varint, packet number.
	header := make([]byte, 0, 32)
	header = append(header, 0xC0|byte(quicPacketNumberLen-1))
	header = binary.BigEndian.AppendUint32(header, quicVersion1)
	header = append(header, byte(quicDCIDLen))
	header = append(header, dcid...)
	header = append(header, 0x00) // SCID length 0
	header = append(header, 0x00) // token length 0 (varint, single byte)
	header = appendQUICVarint(header, uint64(quicInitialLenField))
	pnOffset := len(header)
	for i := 0; i < quicPacketNumberLen; i++ {
		header = append(header, 0x00) // packet number 0
	}

	key, iv, hp := deriveInitialKeys(dcid)
	packet, err := encryptInitial(header, payload, key, iv, hp, pnOffset, 0)
	if err != nil {
		return nil, err
	}
	if len(packet) != quicInitialTotalLen {
		return nil, E.New("amneziawg: QUIC Initial assembled to unexpected size")
	}
	return packet, nil
}

// masqueQUICInitialCPS builds the out-of-order fragmented QUIC Initial decoy and
// emits it as a single static-bytes CPS tag. Uniqueness (fresh DCID + TLS random
// + ephemeral key per call) is baked into the blob at generation time, so no <r>
// randomness is needed — and could not be used anyway, since the DCID feeds the
// key derivation that must happen before encryption.
func masqueQUICInitialCPS(domain, browser string) (string, error) {
	packet, err := buildInitialPacket(domain, browser)
	if err != nil {
		return "", err
	}
	var b cpsBuilder
	b.addBytes(packet)
	return b.String(), nil
}
