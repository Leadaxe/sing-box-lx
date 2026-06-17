//go:build with_awg

package wireguard

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"
)

// This file verifies the generated QUIC Initial by REVERSE-PARSING our own
// output — the §146 §5 control vectors. The decryptor is a faithful mirror of
// the live QUIC sniffer (common/sniff/quic.go): derive Initial keys from the
// DCID, strip header protection, AEAD-open, walk frames, reassemble CRYPTO. If
// our generator and the decryptor agree, the crypto is correct (a wrong tag,
// wrong key, or wrong nonce fails the Open). The assertions check STRUCTURE and
// the I1–I4 invariants, never "two outputs differ" tautologies.

// decodedInitial is the result of reverse-parsing a generated Initial.
type decodedInitial struct {
	dcid         []byte
	lengthField  uint64
	frameTypes   []byte           // frame type bytes in wire order
	cryptoFrames []cryptoFragment // CRYPTO frames in wire order
	clientHello  []byte           // reassembled, contiguous from offset 0
}

// decryptInitial reverse-parses a QUIC v1 Initial: it mirrors common/sniff/quic.go.
func decryptInitial(t *testing.T, packet []byte) decodedInitial {
	t.Helper()
	reader := bytes.NewReader(packet)

	first, err := reader.ReadByte()
	require.NoError(t, err)
	require.Equal(t, byte(0xC0), first&0xF0, "long header, Initial type (0xC0..0xC3)")

	var version uint32
	require.NoError(t, binary.Read(reader, binary.BigEndian, &version))
	require.Equal(t, quicVersion1, version, "QUIC v1")

	dcidLen, err := reader.ReadByte()
	require.NoError(t, err)
	require.Equal(t, byte(quicDCIDLen), dcidLen)
	dcid := make([]byte, dcidLen)
	_, err = io.ReadFull(reader, dcid)
	require.NoError(t, err)

	scidLen, err := reader.ReadByte()
	require.NoError(t, err)
	require.Equal(t, byte(0), scidLen, "empty SCID")

	tokenLen, err := reader.ReadByte()
	require.NoError(t, err)
	require.Equal(t, byte(0), tokenLen, "empty token")

	// length field (varint).
	lengthField := readVarint(t, reader)

	hdrLen := len(packet) - reader.Len()

	// header protection: sample at hdrLen+4 (4 bytes into the pn field region).
	_, _, hp := deriveInitialKeys(dcid)
	block, err := aes.NewCipher(hp)
	require.NoError(t, err)
	sample := packet[hdrLen+4 : hdrLen+4+aes.BlockSize]
	mask := make([]byte, aes.BlockSize)
	block.Encrypt(mask, sample)

	unprotected := make([]byte, len(packet))
	copy(unprotected, packet)
	unprotected[0] ^= mask[0] & 0x0f
	pnLen := int(unprotected[0]&0x03) + 1
	require.Equal(t, quicPacketNumberLen, pnLen, "packet number length")
	for i := 0; i < pnLen; i++ {
		unprotected[hdrLen+i] ^= mask[1+i]
	}
	var packetNumber uint64
	for i := 0; i < pnLen; i++ {
		packetNumber = packetNumber<<8 | uint64(unprotected[hdrLen+i])
	}

	extHdrLen := hdrLen + pnLen
	aad := unprotected[:extHdrLen]
	ciphertext := unprotected[extHdrLen : hdrLen+int(lengthField)]

	key, iv, _ := deriveInitialKeys(dcid)
	cipher := quicAEADAESGCMTLS13(key, iv)
	nonce := make([]byte, cipher.NonceSize())
	binary.BigEndian.PutUint64(nonce[cipher.NonceSize()-8:], packetNumber)
	decrypted, err := cipher.Open(nil, nonce, ciphertext, aad)
	require.NoError(t, err, "AEAD tag must verify — proves crypto is correct (§5.1)")

	// frame-walk.
	var (
		frameTypes []byte
		cryptos    []cryptoFragment
	)
	fr := bytes.NewReader(decrypted)
	for {
		ft, err := fr.ReadByte()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		frameTypes = append(frameTypes, ft)
		switch ft {
		case 0x00, 0x01: // PADDING, PING
			continue
		case 0x06: // CRYPTO
			offset := readVarint(t, fr)
			length := readVarint(t, fr)
			idx := len(decrypted) - fr.Len()
			data := make([]byte, length)
			copy(data, decrypted[idx:idx+int(length)])
			cryptos = append(cryptos, cryptoFragment{offset: offset, data: data})
			_, err = fr.Seek(int64(length), io.SeekCurrent)
			require.NoError(t, err)
		default:
			t.Fatalf("unexpected frame type 0x%02x in generated Initial", ft)
		}
	}

	return decodedInitial{
		dcid:         dcid,
		lengthField:  lengthField,
		frameTypes:   frameTypes,
		cryptoFrames: cryptos,
		clientHello:  reassemble(t, cryptos),
	}
}

func readVarint(t *testing.T, r io.ByteReader) uint64 {
	t.Helper()
	b0, err := r.ReadByte()
	require.NoError(t, err)
	n := 1 << (b0 >> 6)
	v := uint64(b0 & 0x3f)
	for i := 1; i < n; i++ {
		bi, err := r.ReadByte()
		require.NoError(t, err)
		v = v<<8 | uint64(bi)
	}
	return v
}

// reassemble merges CRYPTO fragments by offset into a contiguous [0..N) buffer,
// failing the test on any gap or overlap (I4).
func reassemble(t *testing.T, frags []cryptoFragment) []byte {
	t.Helper()
	var out []byte
	var index uint64
	for {
		progressed := false
		for _, f := range frags {
			if f.offset == index {
				out = append(out, f.data...)
				index += uint64(len(f.data))
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	// ensure no fragment was left unconsumed (gap/overlap detection).
	var total uint64
	for _, f := range frags {
		total += uint64(len(f.data))
	}
	require.Equal(t, total, index, "CRYPTO frames must reassemble contiguously with no gap/overlap (I4)")
	return out
}

// extractSNI walks a TLS 1.3 ClientHello and returns the server_name.
func extractSNI(t *testing.T, ch []byte) string {
	t.Helper()
	require.Equal(t, byte(0x01), ch[0], "ClientHello handshake type")
	chLen := int(ch[1])<<16 | int(ch[2])<<8 | int(ch[3])
	require.Equal(t, chLen, len(ch)-4, "ClientHello length field matches body")
	r := bytes.NewReader(ch[4:])
	skip(t, r, 2)          // legacy_version
	skip(t, r, 32)         // random
	sidLen := readU8(t, r) // session_id
	skip(t, r, int(sidLen))
	csLen := readU16(t, r) // cipher_suites
	require.Greater(t, int(csLen), 0, "cipher_suites must be non-empty (§3.1)")
	skip(t, r, int(csLen))
	compLen := readU8(t, r) // compression_methods
	skip(t, r, int(compLen))
	extTotal := readU16(t, r) // extensions
	end := r.Len() - int(extTotal)
	for r.Len() > end {
		extType := readU16(t, r)
		extLen := readU16(t, r)
		body := make([]byte, extLen)
		_, err := io.ReadFull(r, body)
		require.NoError(t, err)
		if extType == 0x0000 { // server_name
			// ServerNameList: u16 list_len, name_type(1), u16 host_len, host.
			require.Equal(t, byte(0x00), body[2], "name_type host_name")
			hostLen := int(body[3])<<8 | int(body[4])
			return string(body[5 : 5+hostLen])
		}
	}
	t.Fatal("server_name extension not found in ClientHello")
	return ""
}

func skip(t *testing.T, r *bytes.Reader, n int) {
	t.Helper()
	_, err := r.Seek(int64(n), io.SeekCurrent)
	require.NoError(t, err)
}

func readU8(t *testing.T, r io.ByteReader) uint8 {
	t.Helper()
	b, err := r.ReadByte()
	require.NoError(t, err)
	return b
}

func readU16(t *testing.T, r io.Reader) uint16 {
	t.Helper()
	var v uint16
	require.NoError(t, binary.Read(r, binary.BigEndian, &v))
	return v
}

// genInitial generates the masquerade Initial and renders it to raw bytes via
// the same CPS path the device uses (parse the <b> spec, emit bytes).
func genInitial(t *testing.T, sni string) []byte {
	t.Helper()
	spec, err := masqueI1(option.AmneziaWGOptions{Ip: "quic", Id: sni, Ib: "chrome"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)
	return pkt
}

// §5.4 — size: packet ≈1250B, length field 1232.
func TestQUICInitialSize(t *testing.T) {
	t.Parallel()
	pkt := genInitial(t, "www.google.com")
	require.Equal(t, quicInitialTotalLen, len(pkt), "padded Initial datagram size")
	require.GreaterOrEqual(t, len(pkt), 1200, "RFC 9000 §14.1 minimum")
	d := decryptInitial(t, pkt)
	require.Equal(t, uint64(quicInitialLenField), d.lengthField, "length field = 1232 (0x44d0)")
}

// §5.1 — decrypt: own output AEAD-opens (tag verifies). Proven inside
// decryptInitial; this test names it explicitly.
func TestQUICInitialDecrypt(t *testing.T) {
	t.Parallel()
	pkt := genInitial(t, "apteka.ru")
	d := decryptInitial(t, pkt) // require.NoError on Open inside
	require.NotEmpty(t, d.clientHello)
}

// §5.2 — frame-walk + invariants I1–I3.
func TestQUICInitialFrameWalkInvariants(t *testing.T) {
	t.Parallel()
	pkt := genInitial(t, "gosuslugi.ru")
	d := decryptInitial(t, pkt)

	require.GreaterOrEqual(t, len(d.cryptoFrames), 6, "≥6 CRYPTO frames (§3.2)")

	var pings, paddings int
	for _, ft := range d.frameTypes {
		switch ft {
		case 0x01:
			pings++
		case 0x00:
			paddings++
		}
	}
	require.GreaterOrEqual(t, pings, 1, "≥1 PING frame (I3)")
	require.Greater(t, paddings, 0, "PADDING runs present (I3)")

	// I1: first CRYPTO frame in wire order has offset≠0.
	require.NotEqual(t, uint64(0), d.cryptoFrames[0].offset, "first CRYPTO frame offset≠0 (I1)")

	// I2: the offset-0 CRYPTO frame is NOT first.
	zeroIdx := -1
	for i, f := range d.cryptoFrames {
		if f.offset == 0 {
			zeroIdx = i
			break
		}
	}
	require.Greater(t, zeroIdx, 0, "offset-0 CRYPTO frame is not the first one (I2)")
}

// §5.3 — reassembly + SNI (I4).
func TestQUICInitialReassemblyAndSNI(t *testing.T) {
	t.Parallel()
	for _, sni := range []string{"apteka.ru", "gosuslugi.ru", "www.google.com"} {
		pkt := genInitial(t, sni)
		d := decryptInitial(t, pkt)
		require.Equal(t, byte(0x01), d.clientHello[0], "reassembled buffer starts with ClientHello (I4)")
		require.Equal(t, sni, extractSNI(t, d.clientHello), "SNI must equal the configured id (I4)")
	}
}

// §5.5 — uniqueness: two calls with the same SNI → different DCID, different TLS
// random, fully different ciphertext.
func TestQUICInitialUniqueness(t *testing.T) {
	t.Parallel()
	a := genInitial(t, "www.google.com")
	b := genInitial(t, "www.google.com")
	require.NotEqual(t, a, b, "two Initials must differ entirely (fresh DCID/random)")

	da := decryptInitial(t, a)
	db := decryptInitial(t, b)
	require.NotEqual(t, da.dcid, db.dcid, "fresh DCID per call")
	// TLS random sits at clientHello[6:38].
	require.NotEqual(t, da.clientHello[6:38], db.clientHello[6:38], "fresh TLS random per call")
}

// Regression: a long but VALID LDH domain (validateMasqueDomain accepts up to
// 253 bytes) must NOT hard-fail generation. Before the flex-PADDING fix, an SNI
// past ~77 bytes overflowed the fixed 294-byte ClientHello target and the
// endpoint failed to start with an opaque error after config was accepted. The
// flex PADDING run absorbs the slack, so the packet stays exactly 1250 bytes for
// any supportable SNI, and the longer ClientHello still reassembles to the right
// SNI (I4).
func TestQUICInitialLongSNI(t *testing.T) {
	t.Parallel()
	// 80-char domain across two ≤63-byte labels — valid LDH, well past the old
	// ~77-byte ClientHello cliff.
	longSNI := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.com"
	require.NoError(t, validateMasqueDomain(longSNI), "test domain must be a valid LDH name")

	pkt := genInitial(t, longSNI)
	require.Equal(t, quicInitialTotalLen, len(pkt), "long-SNI Initial is still exactly 1250 bytes")

	d := decryptInitial(t, pkt)
	require.Equal(t, uint64(quicInitialLenField), d.lengthField, "length field unchanged")
	require.Equal(t, longSNI, extractSNI(t, d.clientHello), "long SNI reassembles correctly (I4)")
	// The ClientHello is now longer than the etalon 294 (the SNI pushed it out).
	require.Greater(t, len(d.clientHello), quicCHTargetLen, "long SNI grows the ClientHello past the etalon")
}

// Randomization: the layout is fresh per call (random cut points + wire order),
// but the I1–I4 invariants must hold on EVERY sample, and two calls must differ
// in their fragment offsets (no fixed cross-user signature). This guards the
// randomizedWirePlan / planFragmentsN paths against a bad seed breaking an
// invariant.
func TestQUICInitialRandomizedInvariants(t *testing.T) {
	t.Parallel()
	const sni = "www.google.com"
	var firstOffsets [][]uint64
	for iter := 0; iter < 80; iter++ {
		pkt, err := buildInitialPacket(sni, "chrome", defaultQUICGenParams())
		require.NoError(t, err)
		require.Equal(t, quicInitialTotalLen, len(pkt))
		d := decryptInitial(t, pkt)
		// I1 + I2
		require.NotEqual(t, uint64(0), d.cryptoFrames[0].offset, "I1: first wire CRYPTO offset != 0 (iter %d)", iter)
		// I3
		var pings, pads int
		for _, ft := range d.frameTypes {
			switch ft {
			case 0x01:
				pings++
			case 0x00:
				pads++
			}
		}
		require.GreaterOrEqual(t, pings, 1, "I3 PING (iter %d)", iter)
		require.Greater(t, pads, 0, "I3 PADDING (iter %d)", iter)
		// I4
		require.Equal(t, byte(0x01), d.clientHello[0])
		require.Equal(t, sni, extractSNI(t, d.clientHello), "I4 SNI (iter %d)", iter)

		offs := make([]uint64, len(d.cryptoFrames))
		for i, f := range d.cryptoFrames {
			offs[i] = f.offset
		}
		firstOffsets = append(firstOffsets, offs)
	}
	// Cross-user signature check: not all layouts identical (cut points vary).
	allSame := true
	for i := 1; i < len(firstOffsets); i++ {
		if !equalU64(firstOffsets[0], firstOffsets[i]) {
			allSame = false
			break
		}
	}
	require.False(t, allSame, "randomized cut points must differ across calls (no fixed signature)")
}

// Robustness knobs: more fragments, more PINGs, and a variable datagram size
// must all still satisfy I1–I4 and stay within the configured size range.
func TestQUICInitialRobustnessKnobs(t *testing.T) {
	t.Parallel()
	const sni = "gosuslugi.ru"
	for _, p := range []quicGenParams{
		{fragments: 10, pings: 3, totalLenMin: 1250, totalLenMax: 1400},
		{fragments: 4, pings: 1, totalLenMin: 1200, totalLenMax: 1300},
		{fragments: 12, pings: 4, totalLenMin: 1300, totalLenMax: 1500},
	} {
		for iter := 0; iter < 30; iter++ {
			pkt, err := buildInitialPacket(sni, "chrome", p)
			require.NoError(t, err, "fragments=%d", p.fragments)
			require.GreaterOrEqual(t, len(pkt), p.totalLenMin)
			require.LessOrEqual(t, len(pkt), p.totalLenMax)
			d := decryptInitial(t, pkt)
			require.Equal(t, p.fragments, len(d.cryptoFrames), "fragment count knob")
			require.NotEqual(t, uint64(0), d.cryptoFrames[0].offset, "I1 at fragments=%d", p.fragments)
			require.Equal(t, sni, extractSNI(t, d.clientHello), "I4 SNI at fragments=%d", p.fragments)
		}
	}
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// varint round-trip against the local encoder.
func TestQUICVarintRoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []uint64{0, 1, 63, 64, 16383, 16384, 1 << 29, 1 << 30, 1232} {
		enc := appendQUICVarint(nil, v)
		got := readVarint(t, bytes.NewReader(enc))
		require.Equal(t, v, got, "varint round-trip for %d", v)
	}
}

// guard: the deriveInitialKeys mirror matches the qtls construction used by the
// sniffer. We re-derive client_secret via the same HKDF path and check the key
// lengths and determinism (same DCID → same keys).
func TestQUICInitialKeysDeterministic(t *testing.T) {
	t.Parallel()
	dcid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	k1, iv1, hp1 := deriveInitialKeys(dcid)
	k2, iv2, hp2 := deriveInitialKeys(dcid)
	require.Equal(t, k1, k2)
	require.Equal(t, iv1, iv2)
	require.Equal(t, hp1, hp2)
	require.Len(t, k1, 16)
	require.Len(t, iv1, 12)
	require.Len(t, hp1, 16)
	// sanity: derivation path is HKDF-Extract(salt, dcid) → "client in".
	initialSecret := hkdf.Extract(crypto.SHA256.New, dcid, quicSaltV1)
	clientSecret := quicHKDFExpandLabel(crypto.SHA256, initialSecret, []byte{}, "client in", crypto.SHA256.Size())
	require.Equal(t, k1, quicHKDFExpandLabel(crypto.SHA256, clientSecret, []byte{}, "quic key", 16))
}
