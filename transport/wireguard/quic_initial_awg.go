//go:build with_awg

// QUIC Initial masquerade generator — a client's first QUIC packet carrying a
// TLS ClientHello for the masquerade domain (ip=quic, SPECS/TASKS/009).
//
// The i1 is a standalone decoy (src=nil, sent before the WG handshake — see
// amneziawg-go send.go); it never completes a TLS handshake, it only has to be
// a valid QUIC Initial for the DPI and for the WARP endpoint behind it.
//
// What the field runs established (LxBox §146, §617, §618):
//   - A plain WireGuard start to WARP is dropped; the same flow with a QUIC
//     Initial in front passes. The Initial itself is the payload.
//   - The order of CRYPTO frames inside the Initial does not matter (§617:
//     in-order and shuffled pass alike). The earlier "DPI parses the first
//     frame and fails open" rationale was refuted and is gone.
//   - A ClientHello spread over several Initials is dropped on the WARP path
//     (§618), so every profile here puts the whole ClientHello into ONE Initial,
//     oversized past the MTU when it has to be (chrome-full).
//
// Frame layout follows the browser the ClientHello imitates: Chrome's
// QuicChaosProtector (splits, PINGs, spread PADDING, shuffle) for ib=chrome and
// ib=chrome-full, one CRYPTO frame plus PADDING for everything else.
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
	"math/big"

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
// QUIC Initial geometry.
// ---------------------------------------------------------------------------

const (
	quicInitialTotalLen = 1250 // Chrome datagram size (its max packet size; ≥1200 per RFC 9000 §14.1)
	quicInitialLenField = 1232 // varint "length" = pn_len + payload + tag (0x44d0) at 1250, pn_len 1
	quicFirefoxTotalLen = 1252 // Firefox (neqo) datagram size: the QUIC packet plus zero fill
	quicPacketNumberLen = 1    // Chrome packet number length in bytes (pn_len=1)
	quicAEADTagLen      = 16   // AES-128-GCM authentication tag
	quicDCIDLen         = 8    // Destination Connection ID length (fresh per call)

	// quicInitialOversizeSlack is the PADDING budget kept when the ClientHello
	// does not fit the configured datagram size and the Initial grows past it
	// (chrome-full): chaos protection needs room for its extra CRYPTO frame
	// headers and PINGs.
	quicInitialOversizeSlack = 128
)

// ---------------------------------------------------------------------------
// Initial frame layout, per browser.
//
// ib=chrome mirrors quiche's QuicChaosProtector (quic_chaos_protector.cc), the
// only client stack that scrambles its Initial: the ClientHello CRYPTO frame is
// split 2..10 times at random points, 2..10 PING frames are added, the PADDING
// budget is spread at random between frames, and the whole frame list is
// shuffled. Every other stack (Firefox/neqo, curl/ngtcp2, quic-go) sends one
// CRYPTO frame followed by a PADDING tail, so ib=firefox and the generic
// ClientHello use that plain layout.
// ---------------------------------------------------------------------------

// quicGenParams holds the datagram size range for the generator. Chrome sends
// its Initial at its max packet size (1250), hence min==max by default.
type quicGenParams struct {
	totalLenMin int // inclusive lower bound for the datagram size (≥1200)
	totalLenMax int // inclusive upper bound for the datagram size
}

func defaultQUICGenParams() quicGenParams {
	return quicGenParams{totalLenMin: quicInitialTotalLen, totalLenMax: quicInitialTotalLen}
}

// frameKind tags one frame of the Initial payload.
type frameKind int

const (
	frameCrypto frameKind = iota
	framePing
	framePadding
)

// quicFrame is one frame of the Initial payload in wire order.
type quicFrame struct {
	kind   frameKind
	offset uint64 // CRYPTO: stream offset
	data   []byte // CRYPTO: payload slice of the ClientHello
	pad    int    // PADDING: run length in bytes
}

// cryptoFragment is a CRYPTO frame's (offset, data) pair; the test decoder
// collects these from a decrypted packet.
type cryptoFragment struct {
	offset uint64
	data   []byte
}

// cryptoFrameOverhead is quiche's GetMinCryptoFrameSize: type byte plus the
// offset and length varints, excluding the data itself.
func cryptoFrameOverhead(offset uint64, length int) int {
	return 1 + varintLen(offset) + varintLen(uint64(length))
}

// paddingBudget returns how many PADDING bytes remain in a payload of
// payloadLen after one CRYPTO frame carrying the whole ClientHello.
func paddingBudget(ch []byte, payloadLen int) (int, error) {
	budget := payloadLen - cryptoFrameOverhead(0, len(ch)) - len(ch)
	if budget < 0 {
		return 0, E.New("amneziawg: QUIC Initial frames overflow the length field (ClientHello too large)")
	}
	return budget, nil
}

// plainInitialFrames is the one-CRYPTO-plus-PADDING layout.
func plainInitialFrames(ch []byte, payloadLen int) ([]quicFrame, error) {
	budget, err := paddingBudget(ch, payloadLen)
	if err != nil {
		return nil, err
	}
	frames := []quicFrame{{kind: frameCrypto, offset: 0, data: ch}}
	if budget > 0 {
		frames = append(frames, quicFrame{kind: framePadding, pad: budget})
	}
	return frames, nil
}

// chaosInitialFrames mirrors QuicChaosProtector::BuildDataPacket step by step:
// IngestFrames (one CRYPTO + the PADDING budget), SplitCryptoFrame,
// AddPingFrames, SpreadPadding, ReorderFrames. Constants are quiche's.
func chaosInitialFrames(ch []byte, payloadLen int) ([]quicFrame, error) {
	budget, err := paddingBudget(ch, payloadLen)
	if err != nil {
		return nil, err
	}
	if budget == 0 {
		// Chrome skips chaos protection without a padding budget to work with.
		return plainInitialFrames(ch, payloadLen)
	}
	frames := []quicFrame{{kind: frameCrypto, offset: 0, data: ch}}

	// SplitCryptoFrame: kMinAddedCryptoFrames=2, kMaxAddedCryptoFrames=10.
	maxOverhead := cryptoFrameOverhead(uint64(len(ch)), len(ch))
	added, err := randInt(10 + 1 - 2)
	if err != nil {
		return nil, err
	}
	added += 2
	for i := 0; i < added; i++ {
		if budget < maxOverhead {
			break
		}
		idx, err := randInt(len(frames))
		if err != nil {
			return nil, err
		}
		if frames[idx].kind != frameCrypto || len(frames[idx].data) <= 1 {
			continue
		}
		f := frames[idx]
		oldOverhead := cryptoFrameOverhead(f.offset, len(f.data))
		firstLen, err := randInt(len(f.data) - 1)
		if err != nil {
			return nil, err
		}
		firstLen++ // 1..len-1
		second := quicFrame{kind: frameCrypto, offset: f.offset + uint64(firstLen), data: f.data[firstLen:]}
		frames[idx].data = f.data[:firstLen]
		frames = append(frames, second)
		budget -= cryptoFrameOverhead(second.offset, len(second.data))
		budget -= cryptoFrameOverhead(frames[idx].offset, len(frames[idx].data))
		budget += oldOverhead
	}

	// AddPingFrames: kMinAddedPingFrames=2, kMaxAddedPingFrames=10, capped by budget.
	if budget > 0 {
		pings, err := randInt(10 + 1 - 2)
		if err != nil {
			return nil, err
		}
		pings += 2
		if pings > budget {
			pings = budget
		}
		for i := 0; i < pings; i++ {
			frames = append(frames, quicFrame{kind: framePing})
		}
		budget -= pings
	}

	// SpreadPadding: before each frame, a uniform share of what is left.
	for i := 0; i < len(frames); i++ {
		n, err := randInt(budget + 1)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			continue
		}
		frames = append(frames[:i], append([]quicFrame{{kind: framePadding, pad: n}}, frames[i:]...)...)
		i++ // skip over the PADDING frame just inserted
		budget -= n
	}
	if budget > 0 {
		frames = append(frames, quicFrame{kind: framePadding, pad: budget})
	}

	// ReorderFrames: Fisher–Yates over the whole list (no ACK frames here).
	for i := len(frames) - 1; i > 0; i-- {
		j, err := randInt(i + 1)
		if err != nil {
			return nil, err
		}
		frames[i], frames[j] = frames[j], frames[i]
	}
	return frames, nil
}

// quicProfile is the Initial header shape of one client stack, calibrated on
// captures: Chrome 133/147 (quiche) and Firefox 149 (neqo).
type quicProfile struct {
	chaos    bool // QuicChaosProtector frame layout; otherwise one CRYPTO frame
	scidLen  int  // Source Connection ID length: Chrome 0, Firefox 3
	pnLen    int  // packet number length: Chrome 1, Firefox 2
	totalLen int  // datagram size
	// trailing: size the QUIC packet to its frames and zero-fill the datagram
	// after it (neqo); otherwise PADDING frames fill the packet to totalLen.
	trailing bool
}

func quicProfileFor(browser string) quicProfile {
	switch browser {
	case masqueBrowserChrome, masqueBrowserChromeFull:
		return quicProfile{chaos: true, scidLen: 0, pnLen: 1, totalLen: quicInitialTotalLen}
	case masqueBrowserFirefox:
		return quicProfile{chaos: false, scidLen: 3, pnLen: 2, totalLen: quicFirefoxTotalLen, trailing: true}
	default:
		return quicProfile{chaos: false, scidLen: 0, pnLen: 1, totalLen: quicInitialTotalLen}
	}
}

// initialPacketNumber is the first packet number: quiche (Chrome) numbers
// packets from 1; neqo (Firefox) starts at a random value (788 in the Firefox
// 149 capture); quic-go and the generic profile from 0.
func initialPacketNumber(prof quicProfile, browser string) (uint64, error) {
	switch browser {
	case masqueBrowserChrome, masqueBrowserChromeFull:
		return 1, nil
	case masqueBrowserFirefox:
		n, err := randInt(1024)
		return uint64(n), err
	default:
		return 0, nil
	}
}

// initialFrames picks the layout for the profile.
func initialFrames(ch []byte, prof quicProfile, payloadLen int) ([]quicFrame, error) {
	if prof.chaos {
		return chaosInitialFrames(ch, payloadLen)
	}
	return plainInitialFrames(ch, payloadLen)
}

// serializeFrames writes the frames in order; the result must be exactly payloadLen.
func serializeFrames(frames []quicFrame, payloadLen int) ([]byte, error) {
	out := make([]byte, 0, payloadLen)
	for _, f := range frames {
		switch f.kind {
		case frameCrypto:
			out = appendCryptoFrame(out, cryptoFragment{offset: f.offset, data: f.data})
		case framePing:
			out = append(out, 0x01)
		case framePadding:
			for i := 0; i < f.pad; i++ {
				out = append(out, 0x00)
			}
		}
	}
	if len(out) != payloadLen {
		return nil, E.New("amneziawg: QUIC Initial payload did not land on the length field")
	}
	return out, nil
}

// appendCryptoFrame writes a CRYPTO frame: 0x06 ‖ varint(offset) ‖ varint(len) ‖ data.
func appendCryptoFrame(dst []byte, f cryptoFragment) []byte {
	dst = append(dst, 0x06)
	dst = appendQUICVarint(dst, f.offset)
	dst = appendQUICVarint(dst, uint64(len(f.data)))
	return append(dst, f.data...)
}

// randInt returns a uniform random int in [0, n) using crypto/rand. n must be ≥1.
func randInt(n int) (int, error) {
	if n <= 1 {
		return 0, nil
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
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
// the start of the packet number field, AES-ECB it with hp, then XOR the low 4
// bits of the first byte with mask[0] and each of the pnLen packet-number bytes
// with mask[1+i].
func encryptInitial(header, payload, key, iv, hp []byte, pnOffset, pnLen int, pn uint64) ([]byte, error) {
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
	for i := 0; i < pnLen; i++ {
		packet[pnOffset+i] ^= mask[1+i]
	}
	return packet, nil
}

// buildInitialPacket assembles a complete QUIC v1 Initial datagram carrying the
// ClientHello for sni, per the given params and the browser's profile. Fresh
// DCID/SCID, TLS random, ephemeral x25519 key and (for chrome) a fresh chaos
// layout are generated per call, so every invocation yields a unique
// ciphertext and frame layout — the caller is expected to invoke it per
// handshake (see awgDecoyFunc).
func buildInitialPacket(sni, browser string, p quicGenParams) ([]byte, error) {
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

	prof := quicProfileFor(browser)
	scid := make([]byte, prof.scidLen)
	if _, err := rand.Read(scid); err != nil {
		return nil, err
	}
	clientHello, err := buildClientHello(sni, tlsRandom, ecKey.PublicKey().Bytes(), browser, scid)
	if err != nil {
		return nil, err
	}

	// Pick the datagram size: the profile's own, unless the knobs raise it.
	totalLen, err := pickTotalLen(p)
	if err != nil {
		return nil, err
	}
	if prof.totalLen > totalLen {
		totalLen = prof.totalLen
	}

	// Header length depends on the connection-ID lengths and the length-field
	// varint width (2 bytes for any value in [64, 16383], which covers all our
	// sizes). The length field = pn_len + payload + tag.
	const headerFixed = 1 + 4 + 1 + 1 + 1 // first + version + dcidLen + scidLen + tokenLen(0)
	const lenFieldVarintWidth = 2
	headerLen := headerFixed + quicDCIDLen + prof.scidLen + lenFieldVarintWidth + prof.pnLen

	// A ClientHello that does not fit the picked size (chrome-full with the PQ
	// key_share) grows the Initial instead of spilling into a second packet: the
	// whole ClientHello stays in one QUIC packet and the IP layer fragments it.
	if need := headerLen + cryptoFrameOverhead(0, len(clientHello)) + len(clientHello) + quicInitialOversizeSlack + quicAEADTagLen; need > totalLen {
		totalLen = need
	}
	payloadLen := totalLen - headerLen - quicAEADTagLen
	if prof.trailing {
		// neqo sizes the packet to its frames and zero-fills the datagram after it.
		payloadLen = cryptoFrameOverhead(0, len(clientHello)) + len(clientHello)
	}
	lengthField := prof.pnLen + payloadLen + quicAEADTagLen
	if lengthField < 1<<6 || lengthField >= 1<<14 {
		return nil, E.New("amneziawg: QUIC Initial length field outside 2-byte varint range")
	}

	frames, err := initialFrames(clientHello, prof, payloadLen)
	if err != nil {
		return nil, err
	}
	payload, err := serializeFrames(frames, payloadLen)
	if err != nil {
		return nil, err
	}

	// Unprotected long header: first byte 0xC0|(pn_len-1), version 1, DCID,
	// SCID, token len 0, length varint, packet number.
	pn, err := initialPacketNumber(prof, browser)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, headerLen)
	header = append(header, 0xC0|byte(prof.pnLen-1))
	header = binary.BigEndian.AppendUint32(header, quicVersion1)
	header = append(header, byte(quicDCIDLen))
	header = append(header, dcid...)
	header = append(header, byte(prof.scidLen))
	header = append(header, scid...)
	header = append(header, 0x00) // token length 0 (varint, single byte)
	header = appendQUICVarint(header, uint64(lengthField))
	pnOffset := len(header)
	for i := 0; i < prof.pnLen; i++ {
		header = append(header, byte(pn>>(8*(prof.pnLen-1-i))))
	}

	key, iv, hp := deriveInitialKeys(dcid)
	packet, err := encryptInitial(header, payload, key, iv, hp, pnOffset, prof.pnLen, pn)
	if err != nil {
		return nil, err
	}
	if prof.trailing {
		for len(packet) < totalLen {
			packet = append(packet, 0x00)
		}
	}
	if len(packet) != totalLen {
		return nil, E.New("amneziawg: QUIC Initial assembled to unexpected size")
	}
	return packet, nil
}

// pickRange returns a uniform random int in [lo, hi], with lo clamped to floor
// and hi raised to lo when the range is inverted. When lo==hi it is deterministic.
func pickRange(lo, hi, floor int) (int, error) {
	if lo < floor {
		lo = floor
	}
	if hi < lo {
		hi = lo
	}
	if hi == lo {
		return lo, nil
	}
	d, err := randInt(hi - lo + 1)
	if err != nil {
		return 0, err
	}
	return lo + d, nil
}

// pickTotalLen returns a datagram size in [totalLenMin, totalLenMax], clamped to
// the RFC 9000 §14.1 minimum (1200). When min==max it is deterministic.
func pickTotalLen(p quicGenParams) (int, error) {
	return pickRange(p.totalLenMin, p.totalLenMax, 1200)
}

// masqueQUICInitialCPS builds the QUIC Initial decoy and emits it as a single
// static-bytes CPS tag. Uniqueness (fresh DCID + TLS random
// + ephemeral key + random layout per call) is baked into the blob at generation
// time, so no <r> randomness is needed — and could not be used anyway, since the
// DCID feeds the key derivation that must happen before encryption.
func masqueQUICInitialCPS(domain, browser string) (string, error) {
	packet, err := buildInitialPacket(domain, browser, defaultQUICGenParams())
	if err != nil {
		return "", err
	}
	var b cpsBuilder
	b.addBytes(packet)
	return b.String(), nil
}
