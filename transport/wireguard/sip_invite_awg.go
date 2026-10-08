//go:build with_awg

// SIP INVITE masquerade generator (i1).
//
// MODEL. The decoy is the first packet of a real SIP dialog (RFC 3261 §17): a
// complete, self-contained INVITE request (Content-Length: 0, no SDP body) —
// exactly what a UAC sends to start a call. It goes out as one UDP datagram
// before the WireGuard handshake (amneziawg-go send.go, src=nil).
//
// Earlier shapes are gone: one INVITE fragmented across i1/i2 (head in i1, SDP
// body in i2) left each datagram individually malformed since nothing
// reassembles UDP; the later INVITE (i1) + "SIP/2.0 100 Trying" (i2) pair put a
// SERVER response into the client's slot — a UAC never sends 100 Trying, and a
// WireSock-style responder answers the INVITE with its own 100 Trying.
//
// TEMPLATE. The byte layout matches the canonical RFC 3261 §24.2 example exactly
// — header ORDER (INVITE → Via → Max-Forwards → To → From → Call-ID → CSeq →
// Contact → Content-Type → Content-Length) and the THREE-HOST scheme of a real
// call:
//   - callee host (request-URI + To)  — the party being called, a DIFFERENT domain;
//   - caller domain (From)            — our own domain (= the configured id);
//   - caller UA host (Via/Call-ID/Contact) — our endpoint, a `pcNN.<caller-domain>`
//     subdomain (the device, not the domain).
//
// Only the identifiers are randomised (no cross-user signature, unlike a literal
// alice@atlanta.com / bob@biloxi.com replay which is a public DPI beacon): user
// names and the callee/UA hosts are pronounceable PseudoGen strings; branch, tag,
// Call-ID and CSeq are crypto/rand hex/digits. The configured id (LDH-validated,
// CRLF-safe) becomes the caller domain so it stays visible on the wire.
//
// REQUIRES junk packets. This profile is meant to be used with jc/jmin/jmax > 0:
// the masquerade decoy rides out alongside junk in the same pre-handshake burst.
package wireguard

import (
	"strings"
)

// sipDialog holds the per-generation identifiers of the INVITE. All fields are
// baked into static <b> bytes, never <rc>/<rd>, so the headers agree with each
// other within the message.
//
// Three hosts, per the RFC 3261 §24.2 call shape (see file header):
type sipDialog struct {
	calleeHost string // request-URI / To host — the called party's domain
	callerDom  string // From host — our domain (the configured id)
	uaHost     string // Via / Call-ID / Contact host — our UA (pcNN.<callerDom>)
	fromUser   string // caller local part (From / Contact)
	toUser     string // callee local part (request-URI / To)
	fromDisp   string // caller display name (capitalized)
	toDisp     string // callee display name (capitalized)
	branch     string // Via branch suffix (after the z9hG4bK magic cookie)
	tag        string // From tag
	callID     string // Call-ID local part (before @uaHost)
	cseq       string // CSeq sequence number
}

// newSIPDialog builds a fresh dialog. domain may be "" — then a pseudo caller
// domain is generated. The caller has already LDH-validated a non-empty domain;
// it is used as the caller domain (From) so the configured id stays on the wire.
// The callee is an independent pseudo-domain (a real INVITE calls OUT to another
// host), and the UA host is a pcNN subdomain of the caller domain (the device).
func newSIPDialog(domain string) sipDialog {
	callerDom := strings.TrimSuffix(domain, ".")
	if callerDom == "" {
		callerDom = pgDomainHost()
	}
	fromUser := pgUser()
	toUser := pgUser()
	return sipDialog{
		calleeHost: pgDomainHost(),                       // call OUT to a different domain
		callerDom:  callerDom,                            // our domain (= id)
		uaHost:     "pc" + pgDigits(2) + "." + callerDom, // our device, subdomain of our domain
		fromUser:   fromUser,
		toUser:     toUser,
		fromDisp:   sipCapitalize(fromUser),
		toDisp:     sipCapitalize(toUser),
		branch:     pgHex(16), // Via branch (after z9hG4bK)
		tag:        pgDigits(10),
		callID:     pgHex(14), // Call-ID local part
		cseq:       pgDigits(6),
	}
}

// masqueSIPInviteCPS builds the i1 INVITE request: a complete request with no
// body (Content-Length: 0), in the exact RFC 3261 §24.2 header order. Returns
// the CPS string for slot i1.
func masqueSIPInviteCPS(d sipDialog) (string, error) {
	var s strings.Builder
	s.WriteString("INVITE sip:" + d.toUser + "@" + d.calleeHost + " SIP/2.0\r\n")
	s.WriteString("Via: SIP/2.0/UDP " + d.uaHost + ";branch=z9hG4bK" + d.branch + "\r\n")
	s.WriteString("Max-Forwards: 70\r\n")
	s.WriteString("To: " + d.toDisp + " <sip:" + d.toUser + "@" + d.calleeHost + ">\r\n")
	s.WriteString("From: " + d.fromDisp + " <sip:" + d.fromUser + "@" + d.callerDom + ">;tag=" + d.tag + "\r\n")
	s.WriteString("Call-ID: " + d.callID + "@" + d.uaHost + "\r\n")
	s.WriteString("CSeq: " + d.cseq + " INVITE\r\n")
	s.WriteString("Contact: <sip:" + d.fromUser + "@" + d.uaHost + ">\r\n")
	s.WriteString("Content-Type: application/sdp\r\n")
	s.WriteString("Content-Length: 0\r\n")
	s.WriteString("\r\n")

	var b cpsBuilder
	b.addBytes([]byte(s.String()))
	return b.String(), nil
}

// sipCapitalize upper-cases the first byte of an ASCII pseudo-user so it reads
// as a display name (Alice/Bob style). The input is a PseudoGen word: ASCII
// lower-case letters plus '_', so a byte-wise upper of [0] is safe.
func sipCapitalize(s string) string {
	if s == "" {
		return s
	}
	if c := s[0]; c >= 'a' && c <= 'z' {
		return string(c-('a'-'A')) + s[1:]
	}
	return s
}
