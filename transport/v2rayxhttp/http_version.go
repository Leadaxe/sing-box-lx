package v2rayxhttp

import (
	"slices"

	"github.com/sagernet/sing-box/common/badh2"
	"github.com/sagernet/sing-box/common/tls"

	"golang.org/x/net/http2"
)

// lx: SPEC 104 — the HTTP version an XHTTP client speaks.
type httpVersion uint8

const (
	httpVersion11 httpVersion = iota + 1
	httpVersion2
	httpVersion3
)

const (
	nextProtoHTTP11 = "http/1.1"
	nextProtoH3     = "h3"
)

func (v httpVersion) String() string {
	switch v {
	case httpVersion11:
		return "1.1"
	case httpVersion2:
		return "2"
	case httpVersion3:
		return "3"
	default:
		return "unknown"
	}
}

// decideHTTPVersion mirrors Xray's decideHTTPVersion
// (transport/internet/splithttp/dialer.go): REALITY is always HTTP/2 and
// ignores tls.alpn, no TLS is HTTP/1.1, and otherwise only a single-entry
// tls.alpn of exactly "http/1.1" or "h3" moves off HTTP/2.
func decideHTTPVersion(tlsConfig tls.Config, reality bool) httpVersion {
	if reality {
		return httpVersion2
	}
	if tlsConfig == nil {
		return httpVersion11
	}
	nextProtos := tlsConfig.NextProtos()
	if len(nextProtos) != 1 {
		return httpVersion2
	}
	switch nextProtos[0] {
	case nextProtoHTTP11:
		return httpVersion11
	case nextProtoH3:
		return httpVersion3
	default:
		return httpVersion2
	}
}

// realityALPNNeedsH2 reports whether a REALITY config's tls.alpn has to be
// replaced with ["h2"]: REALITY rides HTTP/2 over TCP, and a list without h2
// (["h3"] from a subscription) would never negotiate it. Xray never sends the
// configured ALPN over REALITY at all.
func realityALPNNeedsH2(nextProtos []string) bool {
	return len(nextProtos) > 0 && !slices.Contains(nextProtos, http2.NextProtoTLS)
}

// hideTransportError keeps HTTP library error types from leaving the conn:
// http2.StreamError (lx: SPEC 082) and the quic-go / http3 types (lx: SPEC 104).
func hideTransportError(err error) error {
	return hideQUICError(badh2.HideStreamError(err))
}
