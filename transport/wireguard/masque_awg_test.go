//go:build with_awg

package wireguard

import (
	"encoding/binary"
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

// id is required only for dns/sip (it lands on the wire as QNAME / SIP host);
// for quic/stun it is optional (hostname-less decoy).
func TestMasqueI1DomainRequiredForDNSAndSIP(t *testing.T) {
	t.Parallel()
	for _, proto := range []string{"dns", "sip"} {
		_, err := masqueI1(option.AmneziaWGOptions{Ip: proto})
		require.Error(t, err, "id should be required for ip=%s", proto)
		require.Contains(t, err.Error(), "id (masquerade domain) is required for ip="+proto)
	}
}

// id lands on the wire for quic (SNI), dns (QNAME) and sip (host); only stun is
// hostname-less, so id is optional only for stun.
func TestMasqueI1DomainRequiredForQUIC(t *testing.T) {
	t.Parallel()
	_, err := masqueI1(option.AmneziaWGOptions{Ip: "quic"})
	require.Error(t, err, "id should be required for ip=quic (it becomes the SNI)")
	require.Contains(t, err.Error(), "id (masquerade domain) is required for ip=quic")
}

func TestMasqueI1DomainOptionalForSTUN(t *testing.T) {
	t.Parallel()
	// stun without id must succeed and produce a valid hostname-less decoy.
	stun, err := masqueI1(option.AmneziaWGOptions{Ip: "stun"})
	require.NoError(t, err, "stun without id should be valid")
	require.NotEmpty(t, stun)
	pkt := obfuscateCPS(t, stun)
	require.Equal(t, uint16(0x0101), uint16(pkt[0])<<8|uint16(pkt[1]), "stun-without-id still a Binding Response")

	// quic with an INVALID id is still rejected (LDH applies whenever id is set).
	_, err = masqueI1(option.AmneziaWGOptions{Ip: "quic", Id: "a.com\r\nx"})
	require.Error(t, err, "invalid id must be rejected for quic")
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

// --- DNS EDNS OPT response (parse back as a DNS message) --------------------

func TestMasqueDNSResponseStructure(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Id: "www.google.com", Ip: "dns"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)

	require.GreaterOrEqual(t, len(pkt), 12, "DNS header")

	// Flags 0x8180: QR=1, RD=1, RA=1, NOERROR.
	require.Equal(t, byte(0x81), pkt[2], "QR=1, RD=1")
	require.Equal(t, byte(0x80), pkt[3], "RA=1, RCODE=NOERROR")
	// Section counts: QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT=1.
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[4:6]), "QDCOUNT=1")
	require.Equal(t, uint16(0), binary.BigEndian.Uint16(pkt[6:8]), "ANCOUNT=0")
	require.Equal(t, uint16(0), binary.BigEndian.Uint16(pkt[8:10]), "NSCOUNT=0")
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[10:12]), "ARCOUNT=1 (OPT)")

	// Parse the QNAME from byte 12 and assert it decodes back to the domain.
	name, off := parseDNSName(t, pkt, 12)
	require.Equal(t, "www.google.com", name, "QNAME must encode the configured domain")
	// QTYPE A (0x0001) + QCLASS IN (0x0001).
	require.Equal(t, uint16(1), binary.BigEndian.Uint16(pkt[off:off+2]), "QTYPE A")
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

func TestMasqueSTUNResponseStructure(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Id: "a.com", Ip: "stun"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)

	require.GreaterOrEqual(t, len(pkt), 20, "STUN header")
	// Type = Binding Success Response.
	require.Equal(t, uint16(0x0101), binary.BigEndian.Uint16(pkt[0:2]), "Binding Success Response")
	// Magic cookie.
	require.Equal(t, uint32(0x2112A442), binary.BigEndian.Uint32(pkt[4:8]), "magic cookie")
	// Top 2 bits of byte 0 are 0 (STUN messages start with 00).
	require.Equal(t, byte(0x00), pkt[0]&0xC0, "STUN leading two bits zero")

	// Advertised length must cover exactly the attribute bytes and be 4-aligned.
	msgLen := binary.BigEndian.Uint16(pkt[2:4])
	require.Equal(t, 0, int(msgLen)%4, "STUN message length must be 4-aligned")
	require.Equal(t, len(pkt), 20+int(msgLen), "message length covers exactly the attributes")

	// Walk the attribute TLVs: each must frame itself within the advertised length.
	off := 20
	end := 20 + int(msgLen)
	sawXOR, sawSoftware := false, false
	for off < end {
		require.LessOrEqual(t, off+4, end, "attribute header must fit")
		atype := binary.BigEndian.Uint16(pkt[off : off+2])
		alen := int(binary.BigEndian.Uint16(pkt[off+2 : off+4]))
		require.LessOrEqual(t, off+4+alen, end, "attribute value must fit in message")
		switch atype {
		case 0x0020:
			sawXOR = true
			require.Equal(t, 8, alen, "XOR-MAPPED-ADDRESS IPv4 length")
			require.Equal(t, byte(0x01), pkt[off+5], "address family IPv4")
		case 0x8022:
			sawSoftware = true
			require.Less(t, alen, 128, "SOFTWARE value < 128 (RFC 5389 §15.10)")
			for _, c := range pkt[off+4 : off+4+alen] {
				require.True(t, c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z', "SOFTWARE printable letters")
			}
		}
		// 4-byte alignment of the next attribute.
		padded := (alen + 3) &^ 3
		off += 4 + padded
	}
	require.True(t, sawXOR, "must emit XOR-MAPPED-ADDRESS")
	require.True(t, sawSoftware, "must emit SOFTWARE")
}

// --- SIP response (parse back as SIP) ---------------------------------------

func TestMasqueSIPResponseStructure(t *testing.T) {
	t.Parallel()
	spec, err := masqueI1(option.AmneziaWGOptions{Id: "pbx.example.com", Ip: "sip"})
	require.NoError(t, err)
	pkt := obfuscateCPS(t, spec)
	text := string(pkt)

	require.True(t, strings.HasPrefix(text, "SIP/2.0 200 OK\r\n"), "status line")
	headerEnd := strings.Index(text, "\r\n\r\n")
	require.GreaterOrEqual(t, headerEnd, 0, "header block must terminate with blank line")
	block := text[:headerEnd]
	lines := strings.Split(block, "\r\n")

	// Every header line after the status line must contain ':'.
	for _, line := range lines[1:] {
		require.Contains(t, line, ":", "every SIP header line must contain ':' : %q", line)
	}
	// Mandatory headers in canonical order, with the configured domain as host.
	for _, h := range []string{
		"Via: SIP/2.0/UDP pbx.example.com:5060;branch=z9hG4bK",
		"From: <sip:caller@pbx.example.com>;tag=",
		"To: <sip:callee@pbx.example.com>;tag=",
		"Call-ID: ",
		"CSeq: ",
		"Content-Length: 0",
	} {
		require.Contains(t, text, h, "missing/garbled header: %q", h)
	}
	// Call-ID host part too.
	require.Contains(t, text, "@pbx.example.com\r\n", "Call-ID host part")
	// Nothing after the blank line (Content-Length: 0).
	require.Equal(t, headerEnd+4, len(pkt), "no body after the header block")
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
