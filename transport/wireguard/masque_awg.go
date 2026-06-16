//go:build with_awg

// Masquerade I1 generators — WireSock-style declarative obfuscation.
//
// The protocol packet structures (QUIC short header, EDNS OPT response, STUN
// Binding Success Response, SIP response) are ported from the open-source
// WireSock reference implementation:
//
//	https://github.com/wiresock/amneziawg-install
//	amneziawg-proxy/src/transform.rs (MIT License, Copyright (c) WireSock)
//	amneziawg-proxy/src/quic_handshake.rs::is_valid_sni_hostname
//
// The LDH hostname validator (validateMasqueDomain) mirrors WireSock's
// is_valid_sni_hostname. The QUIC generator lives in masque_quic_awg.go.
//
// IMPORTANT — model difference from WireSock. WireSock is a *server-side* UDP
// proxy that rewrites the leading S1–S4 padding of a datagram whose tail is the
// real (encrypted) WireGuard ciphertext. Its generators seed a PRNG from that
// tail and size length fields to cover it. We instead emit a *standalone* I1
// decoy packet sent before the handshake (amneziawg-go send.go calls
// Obfuscate(buf, nil) — src is nil, so there is no ciphertext tail). We
// therefore port the protocol *structure* but make every decoy self-contained:
// the whole datagram is the CPS output, length fields cover only the bytes we
// actually emit, and entropy comes from the engine's <r N> tag (cryptographic
// randomness, fresh per packet) rather than a payload-seeded LCG. This is an
// honest decoy, not a byte-for-byte replay of WireSock traffic.
package wireguard

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// Masquerade protocol identifiers (option.AmneziaWGOptions.Ip).
const (
	masqueProtoQUIC = "quic"
	masqueProtoDNS  = "dns"
	masqueProtoSTUN = "stun"
	masqueProtoSIP  = "sip"
)

// masqueI1 turns the WireSock-style Id/Ip/Ib masquerade sugar into an
// AmneziaWG I1 "controlled packet sequence" (CPS) string, or returns "" when no
// masquerade is configured (Id/Ip/Ib all empty).
//
// The returned string is a CPS spec understood by the vendored amneziawg-go
// obfuscation engine (newObfChain): a sequence of <b 0xHEX> (static bytes),
// <r N> (N cryptographically-random bytes), <rc N> (N random ASCII letters) and
// <rd N> (N random digits) tags. When the chain is obfuscated with a nil source
// (as I1 decoys are, see amneziawg-go send.go), the whole output is this fixed
// skeleton plus fresh randomness — a self-contained protocol-shaped decoy.
//
// It enforces the validation rules (mutual exclusion with an explicit I1,
// known Ip, known Ib, strict LDH Id) and fails fast with a clear error so a bad
// config is rejected at endpoint build / `sing-box check` rather than silently
// degrading.
func masqueI1(o option.AmneziaWGOptions) (string, error) {
	if o.Id == "" && o.Ip == "" && o.Ib == "" {
		return "", nil
	}

	// Mutual exclusion: id/ip/ib are sugar over I1, so a config that sets both
	// is ambiguous (which one wins?) and almost certainly a mistake.
	if o.I1 != "" {
		return "", E.New("amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other")
	}

	proto := strings.ToLower(strings.TrimSpace(o.Ip))
	if proto == "" {
		return "", E.New("amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic|dns|stun|sip")
	}

	// id is only carried on the wire by dns (QNAME) and sip (host); quic/stun
	// produce a hostname-less decoy and ignore it. So id is required only for
	// dns/sip, and optional for quic/stun. Whenever id IS set (any protocol) it
	// is still LDH-validated — it must never reach the wire un-checked.
	domain := strings.TrimSpace(o.Id)
	if domain != "" {
		if err := validateMasqueDomain(domain); err != nil {
			return "", err
		}
	}

	browser, err := normalizeMasqueBrowser(o.Ib, proto)
	if err != nil {
		return "", err
	}

	switch proto {
	case masqueProtoQUIC:
		return masqueQUICShortHeaderCPS(domain, browser), nil
	case masqueProtoSTUN:
		return masqueSTUNResponseCPS(), nil
	case masqueProtoDNS:
		if domain == "" {
			return "", E.New("amneziawg: id (masquerade domain) is required for ip=dns (it becomes the DNS QNAME)")
		}
		return masqueDNSResponseCPS(domain)
	case masqueProtoSIP:
		if domain == "" {
			return "", E.New("amneziawg: id (masquerade domain) is required for ip=sip (it becomes the SIP host)")
		}
		return masqueSIPResponseCPS(domain)
	default:
		return "", E.New("amneziawg: unknown masquerade protocol ", strconv.Quote(proto), "; one of quic|dns|stun|sip")
	}
}

// validateMasqueDomain enforces a strict LDH (letter-digit-hyphen) hostname,
// mirroring WireSock's quic_handshake.rs::is_valid_sni_hostname. This is a
// SECURITY boundary, not cosmetics: the domain is interpolated into SIP header
// text and encoded into a DNS QNAME, so control bytes (\r \n \0 \t) or SIP/URI
// metacharacters (> ; @ " space) would allow header injection / label
// corruption. Only ASCII alphanumerics, '-' and '_' are allowed; '_' is
// permitted because it is legal in DNS QNAMEs (service labels) and cannot break
// SIP framing.
//
// Rules (per label, split on '.'): non-empty, ≤63 bytes, no leading/trailing
// '-'; whole name non-empty, ≤253 bytes, no leading/trailing '.' (one trailing
// dot is tolerated and trimmed before checks, as a fully-qualified name).
func validateMasqueDomain(domain string) error {
	if domain == "" {
		return E.New("amneziawg: id (masquerade domain) is required when ip/ib is set")
	}
	// Tolerate a single trailing dot (fully-qualified form) before validation.
	name := strings.TrimSuffix(domain, ".")
	if name == "" || len(name) > 253 {
		return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": empty or longer than 253 bytes")
	}
	if strings.HasPrefix(name, ".") {
		return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": leading dot")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": empty label")
		}
		if len(label) > 63 {
			return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": label longer than 63 bytes")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": label with leading/trailing hyphen")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				return E.New("amneziawg: invalid masquerade domain ", strconv.Quote(domain), ": illegal character (only a-z A-Z 0-9 - _ allowed)")
			}
		}
	}
	return nil
}

// masqueBrowser identifiers (option.AmneziaWGOptions.Ib).
const (
	masqueBrowserChrome  = "chrome"
	masqueBrowserFirefox = "firefox"
	masqueBrowserCurl    = "curl"
)

// normalizeMasqueBrowser validates Ib against the accepted set and returns it
// lower-cased, or "" when unset.
//
// HONESTY NOTE: WireSock's real QUIC masquerade is a 1-RTT short header with no
// ClientHello — there is no TLS fingerprint (JA3/JA4) to imitate. We therefore
// do NOT fake a browser fingerprint. Ib is accepted for syntax compatibility
// with WireSock configs and validated; its only effect is to seed a couple of
// otherwise-meaningless QUIC short-header bits (spin / key_phase), which are
// per-packet random in real QUIC anyway. For dns/stun/sip it has no effect.
// Ib is only meaningful (even minimally) for ip=quic.
func normalizeMasqueBrowser(ib, proto string) (string, error) {
	browser := strings.ToLower(strings.TrimSpace(ib))
	if browser == "" {
		return "", nil
	}
	switch browser {
	case masqueBrowserChrome, masqueBrowserFirefox, masqueBrowserCurl:
	default:
		return "", E.New("amneziawg: unknown masquerade browser ", strconv.Quote(ib), "; one of chrome|firefox|curl")
	}
	if proto != masqueProtoQUIC {
		return "", E.New("amneziawg: ib (browser) is only meaningful with ip=quic, got ip=", strconv.Quote(proto))
	}
	return browser, nil
}

// ---------------------------------------------------------------------------
// CPS skeleton builder
// ---------------------------------------------------------------------------

// cpsBuilder accumulates an AmneziaWG CPS spec. Static bytes are emitted as a
// single <b 0xHEX> tag; entropy as <r N>. Tags are space-separated so the
// vendored newObfChain (which scans for <...> tokens and ignores text between
// them) parses them unambiguously.
type cpsBuilder struct {
	parts []string
}

// addBytes appends a <b 0xHEX> static-bytes tag. No-op for an empty slice
// (newBytesObf rejects an empty argument).
func (c *cpsBuilder) addBytes(b []byte) {
	if len(b) == 0 {
		return
	}
	c.parts = append(c.parts, "<b 0x"+hex.EncodeToString(b)+">")
}

// addRand appends a <r N> tag: N cryptographically-random bytes filled by the
// engine at obfuscation time. No-op for n <= 0.
func (c *cpsBuilder) addRand(n int) {
	if n <= 0 {
		return
	}
	c.parts = append(c.parts, fmt.Sprintf("<r %d>", n))
}

// addRandChars appends a <rc N> tag: N random ASCII letters ([a-zA-Z]). Used
// for token/value fields that must be printable text (SIP tokens, STUN
// SOFTWARE). No-op for n <= 0.
func (c *cpsBuilder) addRandChars(n int) {
	if n <= 0 {
		return
	}
	c.parts = append(c.parts, fmt.Sprintf("<rc %d>", n))
}

// addRandDigits appends a <rd N> tag: N random ASCII digits ([0-9]). Used for
// numeric token fields (SIP CSeq). No-op for n <= 0.
func (c *cpsBuilder) addRandDigits(n int) {
	if n <= 0 {
		return
	}
	c.parts = append(c.parts, fmt.Sprintf("<rd %d>", n))
}

func (c *cpsBuilder) String() string {
	return strings.Join(c.parts, "")
}

// ---------------------------------------------------------------------------
// DNS — EDNS OPT response (ported structure from transform.rs::apply_dns_padding)
// ---------------------------------------------------------------------------

const (
	// EDNS OPT advertised UDP payload size (modern resolver default, RFC 6891).
	dnsOptUDPSize uint16 = 1232
	// EDNS option code for the opaque cover payload. 0xFDE9 (65001) is in the
	// IANA local/experimental range (RFC 6891 §6.1.2): resolvers must ignore
	// unknown options, so it carries opaque cover bytes without the zero-content
	// expectation of option code 12 (Padding, RFC 7830). Same code WireSock uses.
	dnsOptCoverCode uint16 = 0xFDE9
	// Number of opaque cover bytes (the encrypted-looking OPT option-data). The
	// standalone decoy has no real ciphertext tail, so we emit a fixed-size
	// random body to give the message a realistic size.
	dnsCoverLen = 40
)

// masqueDNSResponseCPS builds an EDNS-OPT DNS *response* whose Additional
// section carries the cover bytes as the opaque option-data of a single unknown
// EDNS option (code 0xFDE9). Unlike WireSock's root-label question, we encode
// the configured domain as the QNAME so the response mimics an answer to a
// lookup for that domain. The whole datagram parses as one well-formed DNS
// message with no trailing bytes.
//
// Layout: [ Header 12 ][ Question (QNAME + A + IN) ][ OPT RR 11 ][ opt hdr 4 ]
//
//	[ cover <r dnsCoverLen> ]
//
// TXID is <r 2> (random, like a fresh query id). RDLENGTH covers the option
// header + option-data; OPTION-LENGTH covers just the cover bytes.
func masqueDNSResponseCPS(domain string) (string, error) {
	qname, err := encodeDNSName(domain)
	if err != nil {
		return "", err
	}

	// Question section after QNAME: QTYPE A (0x0001) + QCLASS IN (0x0001).
	question := make([]byte, 0, len(qname)+4)
	question = append(question, qname...)
	question = append(question, 0x00, 0x01, 0x00, 0x01)

	const optFixedLen = 11    // root NAME(1)+TYPE(2)+CLASS(2)+TTL(4)+RDLENGTH(2)
	const optOptionHdrLen = 4 // OPTION-CODE(2)+OPTION-LENGTH(2)

	// OPTION-LENGTH covers the cover bytes; RDLENGTH covers option header + data.
	optLen := uint16(dnsCoverLen)
	rdLength := uint16(optOptionHdrLen + dnsCoverLen)

	var hdr cpsBuilder

	// Header (12 B). TXID is emitted as <r 2> separately; the remaining 10 bytes
	// are the static flags + section counts.
	hdr.addRand(2) // TXID
	hdr.addBytes([]byte{
		0x81, 0x80, // QR=1, opcode=0, AA=0, TC=0, RD=1 | RA=1, Z=0, RCODE=NOERROR
		0x00, 0x01, // QDCOUNT = 1
		0x00, 0x00, // ANCOUNT = 0 (NODATA)
		0x00, 0x00, // NSCOUNT = 0
		0x00, 0x01, // ARCOUNT = 1 (the OPT RR)
	})

	// Question section.
	hdr.addBytes(question)

	// OPT RR fixed prefix (11) + option header (4).
	udpHi, udpLo := be16(dnsOptUDPSize)
	rdHi, rdLo := be16(rdLength)
	ocHi, ocLo := be16(dnsOptCoverCode)
	olHi, olLo := be16(optLen)
	opt := []byte{
		0x00,       // NAME: root label (OPT must use the root name)
		0x00, 0x29, // TYPE = OPT (41)
		udpHi, udpLo, // CLASS = requestor UDP size (1232)
		0x00, 0x00, 0x00, 0x00, // TTL: ext-RCODE 0, EDNS version 0, flags 0
		rdHi, rdLo, // RDLENGTH = option header + option-data
		ocHi, ocLo, // OPTION-CODE = 0xFDE9 (unknown)
		olHi, olLo, // OPTION-LENGTH = cover bytes
	}
	hdr.addBytes(opt)

	// Opaque cover bytes (the "encrypted" OPT option-data).
	hdr.addRand(dnsCoverLen)

	return hdr.String(), nil
}

// encodeDNSName encodes an LDH hostname into DNS wire format (length-prefixed
// labels terminated by a root label). The domain is already validated by
// validateMasqueDomain, so every label is 1..63 bytes of LDH+underscore; this
// re-checks the bound defensively (a label length must fit one byte).
func encodeDNSName(domain string) ([]byte, error) {
	name := strings.TrimSuffix(domain, ".")
	labels := strings.Split(name, ".")
	out := make([]byte, 0, len(name)+2)
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return nil, E.New("amneziawg: cannot encode DNS label ", strconv.Quote(label))
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0x00) // root label terminator
	return out, nil
}

// ---------------------------------------------------------------------------
// STUN — Binding Success Response (ported from transform.rs::apply_stun_padding)
// ---------------------------------------------------------------------------

// stunMagicCookie is the RFC 5389 magic cookie.
const stunMagicCookie uint32 = 0x2112A442

// stunSoftwareLen is the length of the SOFTWARE attribute value we emit. It is
// a multiple of 4 (so the TLV ends on a 4-byte boundary) and < 128 per RFC 5389
// §15.10. We use random printable-ASCII filler via <rc>.
const stunSoftwareLen = 16

// masqueSTUNResponseCPS builds a STUN Binding Success Response: type 0x0101,
// the magic cookie, a random 96-bit transaction ID, an XOR-MAPPED-ADDRESS
// attribute (what makes it read as a genuine Binding response) and a SOFTWARE
// attribute filling the advertised message. The advertised length covers
// exactly the attributes written, so a strict parser frames the whole datagram
// with no malformed/trailing bytes.
//
// Unlike WireSock (which derives a stable txn from the payload seed and XORs a
// payload-derived address), the standalone decoy has no payload, so the txn,
// XOR address and SOFTWARE value are random (<r>/<rc>). The XOR-MAPPED-ADDRESS
// value is still a real value — XORing random bytes is still a valid attribute;
// we just don't claim it encodes a meaningful address.
func masqueSTUNResponseCPS() string {
	const xorMappedLen = 12 // TLV header(4) + IPv4 value(8)
	// Message length covers XOR-MAPPED-ADDRESS + SOFTWARE (header 4 + value).
	msgLen := uint16(xorMappedLen + 4 + stunSoftwareLen)

	var b cpsBuilder

	// Header (20 B): type + length + cookie + transaction ID.
	mlHi, mlLo := be16(msgLen)
	ck := be32(stunMagicCookie)
	b.addBytes([]byte{
		0x01, 0x01, // type = Binding Success Response
		mlHi, mlLo, // message length (attributes only)
		ck[0], ck[1], ck[2], ck[3], // magic cookie
	})
	b.addRand(12) // 96-bit transaction ID

	// XOR-MAPPED-ADDRESS (0x0020), IPv4: type + len + reserved + family + xport + xaddr.
	b.addBytes([]byte{
		0x00, 0x20, // attribute type = XOR-MAPPED-ADDRESS
		0x00, 0x08, // attribute length = 8
		0x00, 0x01, // reserved=0, family=IPv4
	})
	b.addRand(2) // X-Port (port ^ cookie high half — random is a valid value)
	b.addRand(4) // X-Address (addr ^ cookie — random is a valid value)

	// SOFTWARE (0x8022): type + len + printable value (random ASCII letters).
	swHi, swLo := be16(uint16(stunSoftwareLen))
	b.addBytes([]byte{
		0x80, 0x22, // attribute type = SOFTWARE
		swHi, swLo, // value length
	})
	b.addRandChars(stunSoftwareLen)

	return b.String()
}

// be16 splits a uint16 into big-endian (hi, lo) bytes. Used so multi-byte
// header fields can be written into a []byte literal without per-field
// constant-overflow conversions.
func be16(v uint16) (hi, lo byte) {
	return byte(v >> 8), byte(v & 0xFF)
}

// be32 splits a uint32 into 4 big-endian bytes.
func be32(v uint32) [4]byte {
	return [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v & 0xFF)}
}

// ---------------------------------------------------------------------------
// SIP — response header block (ported from transform.rs::apply_sip_padding)
// ---------------------------------------------------------------------------

// masqueSIPResponseCPS builds a SIP response header block (RFC 3261 §7.2): a
// `SIP/2.0 200 OK` status line followed by the mandatory Via/From/To/Call-ID/
// CSeq headers and a Content-Length, terminated by a blank line. The configured
// domain is used as the host in the Via/From/To URIs — this is exactly why the
// strict LDH validation matters: an un-validated domain could inject CRLF and
// forge headers.
//
// Per-packet tokens (branch, tags, Call-ID) are random. We build the whole
// header text statically (a single <b> tag) except for the random tokens, which
// are emitted as <rd>/<rc> runs between static <b> fragments. Content-Length is
// 0 (no message body in the decoy) so the datagram frames as one complete SIP
// message with no trailing bytes.
func masqueSIPResponseCPS(domain string) (string, error) {
	host := strings.TrimSuffix(domain, ".")

	var b cpsBuilder
	// Status line + Via up to the branch token.
	b.addBytes([]byte("SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP " + host + ":5060;branch=z9hG4bK"))
	b.addRandChars(16) // branch token (random alnum-ish; <rc> = letters, RFC-safe)
	b.addBytes([]byte(";rport\r\n" +
		"From: <sip:caller@" + host + ">;tag="))
	b.addRandChars(12) // From tag
	b.addBytes([]byte("\r\n" +
		"To: <sip:callee@" + host + ">;tag="))
	b.addRandChars(12) // To tag
	b.addBytes([]byte("\r\nCall-ID: "))
	b.addRandChars(16) // Call-ID local part
	b.addBytes([]byte("@" + host + "\r\n" +
		"CSeq: "))
	b.addRandDigits(5) // CSeq number
	b.addBytes([]byte(" INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"))

	return b.String(), nil
}
