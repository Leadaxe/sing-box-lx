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
// is_valid_sni_hostname. The QUIC generator lives in quic_initial_awg.go (with
// its ClientHello builder in quic_clienthello_awg.go and crypto in
// quic_crypto_awg.go).
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

	// id is carried on the wire by quic (SNI in the ClientHello) and dns (QNAME),
	// where it is REQUIRED. sip uses it as the host but falls back to a generated
	// pseudo-host when empty (so id is optional for sip); stun is hostname-less and
	// ignores it. Whenever id IS set (any protocol) it is still LDH-validated — it
	// must never reach the wire unchecked.
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
		if domain == "" {
			return "", E.New("amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)")
		}
		return masqueQUICInitialCPS(domain, browser)
	case masqueProtoSTUN:
		return masqueSTUNRequestCPS()
	case masqueProtoDNS:
		if domain == "" {
			return "", E.New("amneziawg: id (masquerade domain) is required for ip=dns (it becomes the DNS QNAME)")
		}
		return masqueDNSQueryCPS(domain)
	case masqueProtoSIP:
		// id optional for sip: used as the SIP host when set, else a pseudo-host
		// is generated (PseudoGen). A set id is still LDH-validated above.
		return masqueSIPInviteCPS(domain)
	default:
		return "", E.New("amneziawg: unknown masquerade protocol ", strconv.Quote(proto), "; one of quic|dns|stun|sip")
	}
}

// validateMasqueDomain enforces a strict LDH (letter-digit-hyphen) hostname,
// mirroring WireSock's quic_handshake.rs::is_valid_sni_hostname. This is a
// SECURITY boundary, not cosmetics: the domain is interpolated into SIP header
// text, encoded into a DNS QNAME, and placed in the QUIC ClientHello SNI
// (server_name) extension, so control bytes (\r \n \0 \t) or SIP/URI
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
// HONESTY NOTE: the QUIC masquerade now emits a fragmented QUIC Initial with a
// real ClientHello (see quic_initial_awg.go), but the DPI bypass works on
// out-of-order CRYPTO-frame fragmentation, NOT on a TLS fingerprint (§146 §7).
// We therefore do NOT imitate a specific browser JA3/JA4. Ib is accepted for
// syntax compatibility with WireSock configs and validated, but currently does
// not change the generated ClientHello. For dns/stun/sip it has no effect; ib is
// only meaningful (and even then only as a future hook) for ip=quic.
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
// DNS — EDNS OPT query (a client-initiated lookup; direction-corrected)
// ---------------------------------------------------------------------------
//
// HONEST STATUS. ip=dns emits a DNS QUERY (QR=0) — what a client legitimately
// sends first, fixing the wrong-direction anomaly of the earlier QR=1 response
// (a response sent unsolicited as the first packet is a server-role packet in
// the client's slot; the STUN profile had the same defect). But the decoy still
// goes to the WARP endpoint (a datacenter Cloudflare IP on UDP/2408), which is
// NOT a resolver — raw DNS lives on :53. On the LTE/WARP DPI that motivated §146,
// STUN was blocked as a protocol CLASS toward that destination regardless of
// packet quality, and a DNS query is expected to behave the same (the
// destination, not the direction, is the likely blocker). This profile is NOT
// device-confirmed for WARP — use ip=quic for WARP. ip=dns is kept for other
// providers whose DPI only checks well-formedness, not protocol-to-destination.
//
// Layout (one well-formed DNS query, no trailing bytes):
//
//	[ Header 12 ][ Question (QNAME + HTTPS + IN) ][ OPT RR 11 ][ opt hdr 4 ][ cover ]
//
// TXID is <r 2> (fresh per packet, like a stub resolver). RDLENGTH covers the
// option header + option-data; OPTION-LENGTH covers just the cover bytes.

const (
	// EDNS OPT advertised UDP payload size (modern resolver default, RFC 6891).
	dnsOptUDPSize uint16 = 1232
	// EDNS option code for the opaque cover payload. 0xFDE9 (65001) is in the
	// IANA local/experimental range (RFC 6891 §6.1.2): resolvers must ignore
	// unknown options, so it carries opaque cover bytes without the zero-content
	// expectation of option code 12 (Padding, RFC 7830).
	dnsOptCoverCode uint16 = 0xFDE9
	// Number of opaque cover bytes (the encrypted-looking OPT option-data). The
	// standalone decoy has no real ciphertext tail, so we emit a fixed-size
	// random body to give the message a realistic size.
	dnsCoverLen = 40
	// QTYPE HTTPS (RR type 65, RFC 9460): the most common query a modern browser
	// emits per navigation — the most "expected" query shape on the wire.
	dnsQTypeHTTPS uint16 = 0x0041
)

// masqueDNSQueryCPS builds an EDNS-OPT DNS query (QR=0) for the configured
// domain (QNAME), carrying the cover bytes as the opaque option-data of a single
// unknown EDNS option (code 0xFDE9) in the Additional section — the normal EDNS
// query shape. The whole datagram parses as one well-formed DNS message with no
// trailing bytes.
func masqueDNSQueryCPS(domain string) (string, error) {
	qname, err := encodeDNSName(domain)
	if err != nil {
		return "", err
	}

	// Question section after QNAME: QTYPE HTTPS (0x0041) + QCLASS IN (0x0001).
	qtHi, qtLo := be16(dnsQTypeHTTPS)
	question := make([]byte, 0, len(qname)+4)
	question = append(question, qname...)
	question = append(question, qtHi, qtLo, 0x00, 0x01)

	const optOptionHdrLen = 4 // OPTION-CODE(2)+OPTION-LENGTH(2)

	// OPTION-LENGTH covers the cover bytes; RDLENGTH covers option header + data.
	optLen := uint16(dnsCoverLen)
	rdLength := uint16(optOptionHdrLen + dnsCoverLen)

	var hdr cpsBuilder

	// Header (12 B). TXID is emitted as <r 2> separately; the remaining 10 bytes
	// are the static flags + section counts.
	hdr.addRand(2) // TXID
	hdr.addBytes([]byte{
		0x01, 0x00, // QR=0 (query), opcode=0, AA=0, TC=0, RD=1 | RA=0, Z=0, RCODE=0
		0x00, 0x01, // QDCOUNT = 1
		0x00, 0x00, // ANCOUNT = 0
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
		0x00, 0x00, 0x00, 0x00, // TTL: ext-RCODE 0, EDNS version 0, flags 0 (DO=0)
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
// STUN — magic cookie constant (the Binding Request generator lives in
// stun_request_awg.go; the old Binding Success Response decoy was replaced — a
// response sent as the client's first packet is a wrong-direction anomaly).
// ---------------------------------------------------------------------------

// stunMagicCookie is the RFC 5389 magic cookie.
const stunMagicCookie uint32 = 0x2112A442

// be16 splits a uint16 into big-endian (hi, lo) bytes. Used so multi-byte
// header fields can be written into a []byte literal without per-field
// constant-overflow conversions.
func be16(v uint16) (hi, lo byte) {
	return byte(v >> 8), byte(v & 0xFF)
}

// ---------------------------------------------------------------------------
// SIP — the INVITE request generator lives in sip_invite_awg.go (a `200 OK`
// response sent as the client's first packet was a wrong-direction anomaly,
// like the old STUN/DNS profiles; an INVITE is what a client sends first).
// ---------------------------------------------------------------------------
