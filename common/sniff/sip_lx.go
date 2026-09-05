package sniff

// lx: SPEC 079 — SIP sniffer (UDP packet + TCP stream).
//
// A SIP request starts with a request-line (RFC 3261 §7.1):
//
//	Method SP Request-URI SP SIP/2.0 CRLF
//
// Only requests are recognised — a client never opens with a status-line.
// The host of a sip:/sips: Request-URI becomes metadata.Domain so domain
// rules apply to calls the same way they apply to TLS SNI.

import (
	"bufio"
	"context"
	"io"
	"net/netip"
	"os"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	sipVersion     = "SIP/2.0"
	sipMaxLineLen  = 1024
	sipMinLineLen  = len("ACK sip:a SIP/2.0")
	sipMaxMethod   = 9 // "SUBSCRIBE"
	sipPeekInitial = sipMaxMethod + 1
)

var sipMethods = map[string]bool{
	"INVITE": true, "ACK": true, "BYE": true, "CANCEL": true, "OPTIONS": true,
	"REGISTER": true, "PRACK": true, "SUBSCRIBE": true, "NOTIFY": true,
	"PUBLISH": true, "INFO": true, "REFER": true, "MESSAGE": true, "UPDATE": true,
}

// sipMethodPrefix reports whether b (possibly shorter than a full method) can
// still start a known method followed by a space.
func sipMethodPrefix(b []byte) bool {
	for m := range sipMethods {
		ms := m + " "
		n := len(b)
		if n > len(ms) {
			n = len(ms)
		}
		if string(b[:n]) == ms[:n] {
			return true
		}
	}
	return false
}

// sipParseRequestLine validates the request-line and returns the URI host
// ("" when it is not a sip/sips URI or is an IP literal).
func sipParseRequestLine(line string) (string, bool) {
	if len(line) < sipMinLineLen {
		return "", false
	}
	method, rest, ok := strings.Cut(line, " ")
	if !ok || !sipMethods[method] {
		return "", false
	}
	uri, version, ok := strings.Cut(rest, " ")
	if !ok || version != sipVersion || uri == "" || strings.ContainsAny(uri, " \t") {
		return "", false
	}
	lower := strings.ToLower(uri)
	var hostPart string
	switch {
	case strings.HasPrefix(lower, "sip:"):
		hostPart = uri[4:]
	case strings.HasPrefix(lower, "sips:"):
		hostPart = uri[5:]
	case strings.HasPrefix(lower, "tel:"):
		return "", true
	default:
		return "", false
	}
	if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if end := strings.IndexAny(hostPart, ";?"); end >= 0 {
		hostPart = hostPart[:end]
	}
	if strings.HasPrefix(hostPart, "[") {
		return "", true // IPv6 literal
	}
	if colon := strings.IndexByte(hostPart, ':'); colon >= 0 {
		hostPart = hostPart[:colon]
	}
	if hostPart == "" {
		return "", false
	}
	if _, err := netip.ParseAddr(hostPart); err == nil {
		return "", true
	}
	return strings.ToLower(hostPart), true
}

func sipApply(metadata *adapter.InboundContext, line string) error {
	host, ok := sipParseRequestLine(line)
	if !ok {
		return os.ErrInvalid
	}
	metadata.Protocol = C.ProtocolSIP
	if host != "" {
		metadata.Domain = host
	}
	return nil
}

// SIP detects a SIP request datagram.
func SIP(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	if !sipMethodPrefix(packet) {
		return os.ErrInvalid
	}
	end := strings.Index(string(packet[:min(len(packet), sipMaxLineLen)]), "\r\n")
	if end < 0 {
		return os.ErrInvalid
	}
	return sipApply(metadata, string(packet[:end]))
}

// SIPStream detects a SIP request on a TCP stream.
func SIPStream(_ context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	bReader := bufio.NewReaderSize(reader, sipMaxLineLen)
	head, err := bReader.Peek(sipPeekInitial)
	if !sipMethodPrefix(head) {
		return os.ErrInvalid
	}
	if err != nil {
		return E.Cause1(ErrNeedMoreData, err)
	}
	line, err := bReader.ReadSlice('\n')
	if err != nil {
		if err == bufio.ErrBufferFull {
			return os.ErrInvalid
		}
		return E.Cause1(ErrNeedMoreData, err)
	}
	return sipApply(metadata, strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"))
}
