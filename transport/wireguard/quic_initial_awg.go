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

// quicGenParams are the robustness "knobs" for the QUIC Initial generator. They
// exist so the obfuscation can be escalated WITHOUT a code change if the target
// DPI ever stops being fooled by the current 6-fragment / 1250-byte shape —
// e.g. a DPI that starts keeping a small reassembly buffer is defeated by more
// fragments and a different per-packet layout. Every combination still satisfies
// the I1–I4 invariants (enforced by randomizedWirePlan + planFragmentsN).
//
// The zero value is NOT valid; use defaultQUICGenParams(). All randomness is
// per-call (crypto/rand), so two calls with the same params still produce
// different DCID / TLS random / cut points / wire order — no cross-user
// signature even at fixed params.
type quicGenParams struct {
	fragments   int // number of CRYPTO fragments the ClientHello is cut into (≥2)
	pings       int // number of PING frames interleaved between CRYPTO frames (≥1)
	totalLenMin int // inclusive lower bound for the final datagram size (≥1200)
	totalLenMax int // inclusive upper bound for the final datagram size
}

// defaultQUICGenParams returns the device-proven baseline: 6 fragments, 2 PINGs,
// a fixed 1250-byte datagram (totalLenMin==totalLenMax). This reproduces the
// shape that passed real-DPI testing (§146); only the per-call layout/cutpoints
// are randomized on top of it.
func defaultQUICGenParams() quicGenParams {
	return quicGenParams{
		fragments:   6,
		pings:       2,
		totalLenMin: quicInitialTotalLen,
		totalLenMax: quicInitialTotalLen,
	}
}

// cryptoFragment is one contiguous slice of the ClientHello at a given offset.
type cryptoFragment struct {
	offset uint64
	data   []byte
}

// The device-proven baseline (§146) was 6 fragments at offsets
// [0,236,266,275,283,290] in a fixed wire order with first CRYPTO offset=236.
// That exact layout is now ONE sample of the randomized space below: per call we
// pick random cut points (planFragmentsN) and a random out-of-order wire plan
// (randomizedWirePlan), so no two clients share a fixed fragment signature while
// the I1–I4 invariants always hold.

// planFragmentsN slices the ClientHello into n contiguous fragments at RANDOM
// cut points (instead of the fixed etalonCutpoints), keeping the reassembly
// contiguous and complete (I4). Cut points are n-1 distinct interior offsets in
// [1, len(ch)-1]; fragment i runs [cut[i], cut[i+1]). Every fragment is
// non-empty. Returned fragments are in OFFSET order (fragment 0 = offset 0).
func planFragmentsN(ch []byte, n int) ([]cryptoFragment, error) {
	if n < 2 {
		return nil, E.New("amneziawg: need ≥2 CRYPTO fragments")
	}
	if len(ch) < n {
		return nil, E.New("amneziawg: ClientHello too short for the requested fragment count")
	}
	// Choose n-1 distinct interior cut points in [1, len(ch)-1].
	cutSet := make(map[int]struct{}, n-1)
	interior := len(ch) - 1 // candidate positions 1..len(ch)-1
	for len(cutSet) < n-1 {
		p, err := randInt(interior) // 0..interior-1
		if err != nil {
			return nil, err
		}
		cutSet[p+1] = struct{}{} // map to 1..len(ch)-1
	}
	cuts := make([]int, 0, n+1)
	cuts = append(cuts, 0)
	for p := range cutSet {
		cuts = append(cuts, p)
	}
	cuts = append(cuts, len(ch))
	sortInts(cuts)

	frags := make([]cryptoFragment, n)
	for i := 0; i < n; i++ {
		frags[i] = cryptoFragment{offset: uint64(cuts[i]), data: ch[cuts[i]:cuts[i+1]]}
	}
	return frags, nil
}

// randomizedWirePlan builds a per-call out-of-order wire plan over the given
// offset-ordered fragments, satisfying the invariants by construction:
//
//	I1 — the first CRYPTO frame on the wire has offset≠0;
//	I2 — the offset-0 fragment is not first;
//	I3 — `pings` PING frames and PADDING runs are interleaved between CRYPTO
//	     frames, and exactly one PADDING run is flex (absorbs the slack);
//	I4 — every fragment appears exactly once (a permutation), so the offsets
//	     reassemble contiguously (guaranteed by planFragmentsN).
//
// The CRYPTO order is a random permutation, repaired so fragment 0 is not first.
// PING and PADDING entries are then woven into the gaps between CRYPTO frames at
// random positions, with one PADDING marked flex.
func randomizedWirePlan(frags []cryptoFragment, pings int) ([]planEntry, error) {
	n := len(frags)
	if n < 2 {
		return nil, E.New("amneziawg: need ≥2 CRYPTO fragments for a wire plan")
	}
	if pings < 1 {
		pings = 1
	}

	// Random permutation of fragment indices (Fisher–Yates with crypto/rand).
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j, err := randInt(i + 1)
		if err != nil {
			return nil, err
		}
		order[i], order[j] = order[j], order[i]
	}
	// I1+I2: ensure the offset-0 fragment (index 0) is not first on the wire.
	if order[0] == 0 {
		// swap it with the second slot; n≥2 so this slot exists and is ≠ index 0.
		order[0], order[1] = order[1], order[0]
	}

	// Build CRYPTO entries in the permuted order.
	cryptoEntries := make([]planEntry, n)
	for i, idx := range order {
		cryptoEntries[i] = planEntry{kind: frameCrypto, cryptoIdx: idx}
	}

	// Filler entries: `pings` PINGs + a few PADDING runs (one flex). We use
	// (pings + 2) PADDING runs so there is always padding both before the flex
	// and around the PINGs; the flex run is randomly placed among them.
	padRuns := pings + 2
	flexPick, err := randInt(padRuns)
	if err != nil {
		return nil, err
	}
	fillers := make([]planEntry, 0, pings+padRuns)
	for i := 0; i < pings; i++ {
		fillers = append(fillers, planEntry{kind: framePing})
	}
	for i := 0; i < padRuns; i++ {
		e := planEntry{kind: framePadding}
		if i == flexPick {
			e.padFlex = true
		} else {
			// small fixed run; the flex run absorbs the bulk to hit targetLen.
			sz, err := randInt(48)
			if err != nil {
				return nil, err
			}
			e.padLen = 8 + sz // 8..55 bytes
		}
		fillers = append(fillers, e)
	}

	// Weave: place CRYPTO frames in order, inserting fillers at random gaps. We
	// must keep CRYPTO relative order (it carries I1/I2); fillers go between them.
	// Build a sequence of n "slots after each CRYPTO" plus one leading slot, and
	// drop each filler into a random slot — but never before the first CRYPTO
	// (a leading PADDING/PING is fine for realism, but the FIRST frame must be the
	// offset≠0 CRYPTO for I1). So filler slots are the n gaps AFTER crypto[0..n-1].
	plan := make([]planEntry, 0, n+len(fillers))
	gaps := make([][]planEntry, n) // fillers to emit AFTER cryptoEntries[i]
	for _, f := range fillers {
		slot, err := randInt(n) // 0..n-1 → after crypto[slot]
		if err != nil {
			return nil, err
		}
		gaps[slot] = append(gaps[slot], f)
	}
	for i, c := range cryptoEntries {
		plan = append(plan, c)
		plan = append(plan, gaps[i]...)
	}
	return plan, nil
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

// sortInts is a tiny ascending insertion sort (slices are ≤ a dozen elements).
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
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
// fragmented ClientHello for sni, per the given params. Fresh DCID, TLS random,
// ephemeral x25519 key, random cut points and a random wire order are generated
// per call, so every invocation yields a unique ciphertext AND a unique
// fragment layout — no cross-user signature even at fixed params.
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

	clientHello, err := buildClientHello(sni, tlsRandom, ecKey.PublicKey().Bytes(), browser)
	if err != nil {
		return nil, err
	}

	// Random per-call cut points (I4 preserved) and out-of-order wire plan
	// (I1–I3 preserved by construction).
	frags, err := planFragmentsN(clientHello, p.fragments)
	if err != nil {
		return nil, err
	}
	plan, err := randomizedWirePlan(frags, p.pings)
	if err != nil {
		return nil, err
	}

	// Pick the datagram size from the configured range (a knob: varying the size
	// between packets removes the fixed-1250 tell). pn_len is fixed at 1.
	totalLen, err := pickTotalLen(p)
	if err != nil {
		return nil, err
	}

	// Header length depends on the DCID length and the length-field varint width.
	// Compute the header size, then the length field = pn_len + payload + tag, and
	// the payload region = totalLen - headerLen - tag. The length-field varint is
	// 2 bytes for any value in [64, 16383], which covers all our sizes (≥1200).
	const headerFixed = 1 + 4 + 1 + 1 + 1 // first + version + dcidLen + scidLen(0) + tokenLen(0)
	const lenFieldVarintWidth = 2
	headerLen := headerFixed + quicDCIDLen + lenFieldVarintWidth + quicPacketNumberLen
	payloadLen := totalLen - headerLen - quicAEADTagLen
	lengthField := quicPacketNumberLen + payloadLen + quicAEADTagLen
	if lengthField < 1<<6 || lengthField >= 1<<14 {
		return nil, E.New("amneziawg: QUIC Initial length field outside 2-byte varint range")
	}

	payload, err := buildInitialPayload(frags, plan, payloadLen)
	if err != nil {
		return nil, err
	}

	// Unprotected long header: first byte 0xC0|(pn_len-1), version 1, DCID,
	// SCID len 0, token len 0, length varint, packet number.
	header := make([]byte, 0, headerLen)
	header = append(header, 0xC0|byte(quicPacketNumberLen-1))
	header = binary.BigEndian.AppendUint32(header, quicVersion1)
	header = append(header, byte(quicDCIDLen))
	header = append(header, dcid...)
	header = append(header, 0x00) // SCID length 0
	header = append(header, 0x00) // token length 0 (varint, single byte)
	header = appendQUICVarint(header, uint64(lengthField))
	pnOffset := len(header)
	for i := 0; i < quicPacketNumberLen; i++ {
		header = append(header, 0x00) // packet number 0
	}

	key, iv, hp := deriveInitialKeys(dcid)
	packet, err := encryptInitial(header, payload, key, iv, hp, pnOffset, 0)
	if err != nil {
		return nil, err
	}
	if len(packet) != totalLen {
		return nil, E.New("amneziawg: QUIC Initial assembled to unexpected size")
	}
	return packet, nil
}

// pickTotalLen returns a datagram size in [totalLenMin, totalLenMax], clamped to
// the RFC 9000 §14.1 minimum (1200). When min==max it is deterministic.
func pickTotalLen(p quicGenParams) (int, error) {
	lo, hi := p.totalLenMin, p.totalLenMax
	if lo < 1200 {
		lo = 1200
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

// masqueQUICInitialCPS builds the out-of-order fragmented QUIC Initial decoy and
// emits it as a single static-bytes CPS tag. Uniqueness (fresh DCID + TLS random
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
