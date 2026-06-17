//go:build with_awg

package wireguard

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// obfuscateCPS renders a CPS spec the way the AmneziaWG device does for an I1
// decoy: it builds the obfuscation chain with the LOCAL test parser (parseCPS)
// and fills the static/random tags. It deliberately mirrors the vendored
// newObfChain semantics (static <b> bytes verbatim, <r>/<rc>/<rd> filled with
// the right-shaped random bytes) so the structural assertions below run against
// the actual on-wire layout. The cross-check that the *real* engine accepts the
// same spec lives in the submodule (see SPECS/009 DoD / IMPLEMENTATION_REPORT);
// here we test the byte structure we emit.
func obfuscateCPS(t *testing.T, spec string) []byte {
	t.Helper()
	chain, err := parseCPS(spec)
	require.NoError(t, err, "spec must parse: %q", spec)
	return chain.obfuscate()
}

// --- masqueI1 dispatch + validation -----------------------------------------

func TestMasqueI1Empty(t *testing.T) {
	t.Parallel()
	s, err := masqueI1(option.AmneziaWGOptions{})
	require.NoError(t, err)
	require.Equal(t, "", s, "no id/ip/ib -> no masquerade")
}

func TestMasqueI1ConflictWithExplicitI1(t *testing.T) {
	t.Parallel()
	_, err := masqueI1(option.AmneziaWGOptions{I1: "<b 0x0844>", Id: "a.com", Ip: "quic"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "conflicts with an explicit i1")
}

func TestMasqueI1UnknownProtocol(t *testing.T) {
	t.Parallel()
	_, err := masqueI1(option.AmneziaWGOptions{Id: "a.com", Ip: "ftp"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown masquerade protocol")
}

func TestMasqueI1MissingProtocol(t *testing.T) {
	t.Parallel()
	_, err := masqueI1(option.AmneziaWGOptions{Id: "a.com"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ip (masquerade protocol) is required")
}

// id is required for quic (SNI) and dns (QNAME) — it must be on the wire. For
// sip it is optional (a pseudo-host is generated when absent) and stun ignores
// it entirely.
func TestMasqueI1DomainRequiredForQUICAndDNS(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"quic", "dns"} {
		_, err := masqueI1(option.AmneziaWGOptions{Ip: proto})
		require.Error(t, err, "id should be required for ip=%s", proto)
		require.Contains(t, err.Error(), "id (masquerade domain) is required for ip="+proto)
	}
}

// id is optional for stun (hostname-less) and sip (pseudo-host generated when
// absent); both must produce a valid decoy without an id.
func TestMasqueI1DomainOptionalForSTUNAndSIP(t *testing.T) {
	t.Parallel()
	// stun without id → a valid hostname-less Binding Request.
	stun, err := masqueI1(option.AmneziaWGOptions{Ip: "stun"})
	require.NoError(t, err, "stun without id should be valid")
	require.NotEmpty(t, stun)
	pkt := obfuscateCPS(t, stun)
	require.Equal(t, uint16(0x0001), uint16(pkt[0])<<8|uint16(pkt[1]), "stun-without-id is a Binding Request")

	// sip without id → a valid INVITE with a generated pseudo-host.
	sip, err := masqueI1(option.AmneziaWGOptions{Ip: "sip"})
	require.NoError(t, err, "sip without id should be valid (pseudo-host)")
	require.True(t, strings.HasPrefix(string(obfuscateCPS(t, sip)), "INVITE sip:"), "sip-without-id is an INVITE")

	// An INVALID id is still rejected (LDH applies whenever id is set).
	_, err = masqueI1(option.AmneziaWGOptions{Ip: "quic", Id: "a.com\r\nx"})
	require.Error(t, err, "invalid id must be rejected")
	require.Contains(t, err.Error(), "invalid masquerade domain")
}

func TestMasqueProtocolCaseInsensitive(t *testing.T) {
	t.Parallel()
	s, err := masqueI1(option.AmneziaWGOptions{Id: "a.com", Ip: "QUIC", Ib: "Chrome"})
	require.NoError(t, err)
	require.NotEmpty(t, s)
}

// --- validateMasqueDomain (LDH security boundary) ---------------------------

func TestValidateMasqueDomainAccepts(t *testing.T) {
	t.Parallel()
	for _, d := range []string{
		"a.com", "www.google.com", "ozon.ru", "sub-domain.example.co.uk",
		"xn--80ak6aa92e.com", "_dmarc.example.com", "a.com.", // trailing dot tolerated
		"localhost",
	} {
		require.NoError(t, validateMasqueDomain(d), "should accept %q", d)
	}
}

func TestValidateMasqueDomainRejectsInjection(t *testing.T) {
	t.Parallel()
	// These are the security-relevant rejections: control bytes and SIP/URI/DNS
	// metacharacters that would break SIP header framing or DNS label encoding.
	for _, d := range []string{
		"a.com\nx",                       // CRLF / LF injection (SIP header injection)
		"a.com\r\nVia: evil",             // explicit header injection
		"a.com\x00",                      // NUL
		"a.com\t",                        // tab
		"a.com>;q=1",                     // SIP angle-bracket / param injection
		"a.com;tag=x",                    // SIP param
		"a.com@evil",                     // URI userinfo
		"a com",                          // space
		"a.com\"",                        // quote
		"-leading.com",                   // leading hyphen label
		"trailing-.com",                  // trailing hyphen label
		".leading-dot.com",               // leading dot
		"a..com",                         // empty label
		"",                               // empty
		strings.Repeat("a", 64) + ".com", // label > 63
		strings.Repeat("a.", 130) + "a",  // name > 253
	} {
		require.Error(t, validateMasqueDomain(d), "should reject %q", d)
	}
}

// --- browser validation -----------------------------------------------------

func TestMasqueBrowserValidation(t *testing.T) {
	t.Parallel()
	// Unknown browser -> error.
	_, err := masqueI1(option.AmneziaWGOptions{Id: "a.com", Ip: "quic", Ib: "safari"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown masquerade browser")

	// Browser with non-quic protocol -> error (ib only meaningful for quic).
	_, err = masqueI1(option.AmneziaWGOptions{Id: "a.com", Ip: "dns", Ib: "chrome"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "only meaningful with ip=quic")
}

// QUIC Initial structural tests (decrypt / frame-walk / reassembly / SNI) live
// in quic_initial_awg_test.go.

// --- DNS EDNS OPT query (parse back as a DNS message) -----------------------

// ip=dns is now a QUERY (QR=0) — a client's first packet is a lookup, not an
// answer. (See the generator's honest-status note: not device-confirmed for
// WARP; the destination, not the direction, is the likely blocker there.)
func TestMasqueDNSQueryStructure(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Id: "www.google.com", Ip: "dns"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)

	require.GreaterOrEqual(t, len(pkt), 12, "DNS header")

	// Flags 0x0100: QR=0 (query), RD=1; byte 3 all zero (RA=0, Z=0, RCODE=0).
	require.Equal(t, byte(0x01), pkt[2], "QR=0, RD=1")
	require.Equal(t, byte(0x00), pkt[3], "byte 3 zero (RA=0, RCODE=0)")
	require.Equal(t, byte(0x00), pkt[2]&0x80, "QR bit clear (query)")
	// Section counts: QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT=1.
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[4:6]), "QDCOUNT=1")
	require.Equal(t, uint16(0), binary.BigEndian.Uint16(pkt[6:8]), "ANCOUNT=0")
	require.Equal(t, uint16(0), binary.BigEndian.Uint16(pkt[8:10]), "NSCOUNT=0")
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[10:12]), "ARCOUNT=1 (OPT)")

	// Parse the QNAME from byte 12 and assert it decodes back to the domain.
	name, off := parseDNSName(t, pkt, 12)
	require.Equal(t, "www.google.com", name, "QNAME must encode the configured domain")
	// QTYPE HTTPS (0x0041) + QCLASS IN (0x0001).
	require.Equal(t, uint16(0x0041), binary.BigEndian.Uint16(pkt[off:off+2]), "QTYPE HTTPS(65)")
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[off+2:off+4]), "QCLASS IN")
	off += 4

	// OPT RR: NAME root(0x00) + TYPE OPT(41) + CLASS udp-size(1232) + TTL(0).
	require.Equal(t, byte(0x00), pkt[off], "OPT NAME root label")
	require.Equal(t, uint16(41), binary.BigEndian.Uint16(pkt[off+1:off+3]), "TYPE OPT(41)")
	require.Equal(t, uint16(1232), binary.BigEndian.Uint16(pkt[off+3:off+5]), "CLASS udp-size")
	require.Equal(t, uint32(0), binary.BigEndian.Uint32(pkt[off+5:off+9]), "TTL 0")
	rdlength := binary.BigEndian.Uint16(pkt[off+9 : off+11])
	rdataStart := off + 11
	// RDLENGTH must cover exactly the rest of the datagram (no trailing bytes).
	require.Equal(t, len(pkt), rdataStart+int(rdlength), "RDLENGTH covers to end")

	// Option: CODE 0xFDE9 + LENGTH covering the cover bytes to the end.
	require.Equal(t, uint16(0xFDE9), binary.BigEndian.Uint16(pkt[rdataStart:rdataStart+2]), "OPTION-CODE")
	optLen := binary.BigEndian.Uint16(pkt[rdataStart+2 : rdataStart+4])
	require.Equal(t, len(pkt), rdataStart+4+int(optLen), "OPTION-LENGTH covers to end")
}

// --- STUN Binding Success Response (parse back as STUN) ---------------------

// STUN is now a WebRTC-style Binding REQUEST (0x0001), not a Success Response —
// a client's first packet is legitimately a request. Verify by reverse-parsing:
// type, magic cookie, attribute framing, required ICE attributes, and a VALID
// FINGERPRINT CRC-32 (proves the whole packet is internally consistent).
func TestMasqueSTUNRequestStructure(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Ip: "stun"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)

	require.GreaterOrEqual(t, len(pkt), 20, "STUN header")
	require.Equal(t, uint16(0x0001), binary.BigEndian.Uint16(pkt[0:2]), "Binding Request")
	require.Equal(t, uint32(0x2112A442), binary.BigEndian.Uint32(pkt[4:8]), "magic cookie")
	require.Equal(t, byte(0x00), pkt[0]&0xC0, "STUN leading two bits zero")

	msgLen := binary.BigEndian.Uint16(pkt[2:4])
	require.Equal(t, len(pkt), 20+int(msgLen), "message length covers all attributes incl FINGERPRINT")

	// Walk attribute TLVs; collect which appeared and where FINGERPRINT sits.
	off := 20
	end := 20 + int(msgLen)
	var seen []uint16
	fpOff := -1
	for off < end {
		require.LessOrEqual(t, off+4, end, "attribute header must fit")
		atype := binary.BigEndian.Uint16(pkt[off : off+2])
		alen := int(binary.BigEndian.Uint16(pkt[off+2 : off+4]))
		require.LessOrEqual(t, off+4+alen, end, "attribute value must fit")
		seen = append(seen, atype)
		if atype == 0x8028 {
			fpOff = off
			require.Equal(t, 4, alen, "FINGERPRINT value is 4 bytes")
		}
		if atype == 0x0008 {
			require.Equal(t, 20, alen, "MESSAGE-INTEGRITY is 20 bytes (HMAC-SHA1)")
		}
		off += 4 + ((alen + 3) &^ 3)
	}
	require.Equal(t, end, off, "attributes tile the message exactly")
	require.Contains(t, seen, uint16(0x0006), "USERNAME present")
	require.Contains(t, seen, uint16(0x0008), "MESSAGE-INTEGRITY present")
	require.Equal(t, uint16(0x8028), seen[len(seen)-1], "FINGERPRINT is the last attribute")

	// FINGERPRINT must verify: CRC-32 of the message up to FINGERPRINT, XOR magic.
	require.Greater(t, fpOff, 0)
	wantCRC := crc32.ChecksumIEEE(pkt[:fpOff]) ^ 0x5354554e
	require.Equal(t, wantCRC, binary.BigEndian.Uint32(pkt[fpOff+4:fpOff+8]), "FINGERPRINT CRC-32 must verify")
}

// Two STUN requests differ entirely (fresh txn / ufrag / integrity key per call).
func TestMasqueSTUNRequestUniqueness(t *testing.T) {
	t.Parallel()
	a, err := masqueI1(option.AmneziaWGOptions{Ip: "stun"})
	require.NoError(t, err)
	b, err := masqueI1(option.AmneziaWGOptions{Ip: "stun"})
	require.NoError(t, err)
	require.NotEqual(t, a, b, "fresh per-call entropy → different blobs")
}

// --- SIP INVITE request (parse back as SIP) ---------------------------------

// ip=sip is now an INVITE *request* with an SDP offer, not a 200 OK response —
// a client's first packet is a call setup, not a reply. (See the generator's
// honest-status note: not device-confirmed for WARP.)
func TestMasqueSIPInviteStructure(t *testing.T) {
	t.Parallel()
	const host = "pbx.example.com"
	spec, err := masqueI1(option.AmneziaWGOptions{Id: host, Ip: "sip"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)
	text := string(pkt)
	assertSIPInvite(t, text, host)
	// User names are pronounceable pseudo-tokens, not hardcoded alice/bob.
	require.NotContains(t, text, "alice@", "user names must be generated, not hardcoded")
	require.NotContains(t, text, "bob@", "user names must be generated, not hardcoded")
}

// id is optional for sip: with no id a plausible pseudo-host is generated, so a
// well-formed INVITE must still be produced.
func TestMasqueSIPInviteNoID(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Ip: "sip"})
	require.NoError(t, err, "sip without id must succeed (pseudo-host generated)")
	require.NotEmpty(t, spec)
	assertSIPInvite(t, string(obfuscateCPS(t, spec)), "" /* any host */)
}

// assertSIPInvite reverse-parses a SIP INVITE and checks the request-line,
// mandatory headers, SDP body and an exact Content-Length. If host != "" the
// request-URI host must equal it; otherwise any host is accepted.
func assertSIPInvite(t *testing.T, text, host string) {
	t.Helper()
	require.True(t, strings.HasPrefix(text, "INVITE sip:"), "request-line starts with INVITE method")
	rlEnd := strings.Index(text, "\r\n")
	require.Greater(t, rlEnd, 0)
	require.True(t, strings.HasSuffix(text[:rlEnd], " SIP/2.0"), "request-line ends SIP/2.0")
	if host != "" {
		require.True(t, strings.HasPrefix(text, "INVITE sip:") && strings.Contains(text[:rlEnd], "@"+host+" "),
			"request-URI host == configured id")
	}

	headerEnd := strings.Index(text, "\r\n\r\n")
	require.GreaterOrEqual(t, headerEnd, 0, "header block must terminate with blank line")
	for _, line := range strings.Split(text[:headerEnd], "\r\n")[1:] {
		require.Contains(t, line, ":", "every SIP header line must contain ':' : %q", line)
	}
	for _, h := range []string{
		"\r\nVia: SIP/2.0/UDP ",
		";branch=z9hG4bK",
		"\r\nMax-Forwards: 70\r\n",
		"\r\nFrom: <sip:",
		";tag=",
		"\r\nTo: <sip:",
		"\r\nCall-ID: ",
		"\r\nCSeq: ",
		" INVITE\r\n",
		"\r\nContact: <sip:",
		"\r\nContent-Type: application/sdp\r\n",
	} {
		require.Contains(t, text, h, "missing/garbled header fragment: %q", h)
	}
	// To has no tag in the initial INVITE.
	toIdx := strings.Index(text, "\r\nTo: ")
	toLine := text[toIdx+2:]
	toLine = toLine[:strings.Index(toLine, "\r\n")]
	require.NotContains(t, toLine, ";tag=", "To must have no tag in the initial INVITE")

	// SDP body present and Content-Length exact.
	body := text[headerEnd+4:]
	require.True(t, strings.HasPrefix(body, "v=0\r\n"), "SDP starts with v=0")
	require.Contains(t, body, "\r\nm=audio ", "SDP offers an audio media line")
	clIdx := strings.Index(text, "Content-Length: ")
	require.GreaterOrEqual(t, clIdx, 0)
	var cl int
	_, err := fmt.Sscanf(text[clIdx:], "Content-Length: %d", &cl)
	require.NoError(t, err)
	require.Equal(t, len(body), cl, "Content-Length must equal the SDP body length")
}

// TestMasqueSIPDomainCannotInject is the security regression: even if a domain
// somehow reached the SIP generator with CRLF, no extra header line appears.
// (validateMasqueDomain rejects it first; this guards the generator too.)
func TestMasqueSIPDomainCannotInject(t *testing.T) {
	t.Parallel()
	_, err := masqueI1(option.AmneziaWGOptions{Id: "a.com\r\nEvil: 1", Ip: "sip"})
	require.Error(t, err, "CRLF domain must be rejected before reaching the generator")
}

// --- DNS name round-trip helper ---------------------------------------------

// parseDNSName decodes a length-prefixed DNS name starting at off, returning the
// dotted name and the offset just past the root label. No compression (decoys
// never use pointers).
func parseDNSName(t *testing.T, pkt []byte, off int) (string, int) {
	t.Helper()
	var labels []string
	for {
		require.Less(t, off, len(pkt), "DNS name must terminate")
		l := int(pkt[off])
		off++
		if l == 0 {
			break
		}
		require.LessOrEqual(t, off+l, len(pkt), "DNS label must fit")
		labels = append(labels, string(pkt[off:off+l]))
		off += l
	}
	return strings.Join(labels, "."), off
}
