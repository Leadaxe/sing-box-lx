package option

import "github.com/sagernet/sing/common/json/badoption"

// V2RayXHTTPOptions configures the XHTTP (Xray "splithttp"/"xhttp") client
// transport. It is a sing-box-lx downstream addition; see SPECS/002. JSON keys
// are snake_case to match Xray's stream settings and the rest of sing-box.
//
// Modes (Xray-compatible):
//   - "auto"       : pick a mode automatically (currently behaves as stream-one).
//   - "packet-up"  : separate GET download stream + sequential POST upload packets.
//   - "stream-up"  : single streamed POST upload + separate GET download stream.
//   - "stream-one" : a single bidirectional HTTP stream (request body up,
//     response body down) — the closest analogue to httpupgrade.
type V2RayXHTTPOptions struct {
	// Host overrides the HTTP Host header (defaults to the TLS SNI or the
	// server address when empty).
	Host string `json:"host,omitempty"`
	// Path is the request path prefix; the session id (and, for packet-up,
	// the upload sequence number) are appended to it.
	Path string `json:"path,omitempty"`
	// Mode selects the XHTTP transport mode: auto|packet-up|stream-up|stream-one.
	Mode string `json:"mode,omitempty"`
	// Headers are extra request headers sent on every XHTTP request.
	Headers badoption.HTTPHeader `json:"headers,omitempty"`
	// XPaddingBytes is the inclusive byte-length range of the X-Padding header
	// used to defeat traffic-shape fingerprinting, expressed as Xray does it:
	// "min-max" (e.g. "100-1000") or a single integer. Empty defaults to
	// "100-1000".
	XPaddingBytes string `json:"x_padding_bytes,omitempty"`
	// NoGRPCHeader, when true, omits the gRPC-style headers Xray adds in some
	// modes; kept for forward-compatibility with Xray stream settings.
	NoGRPCHeader bool `json:"no_grpc_header,omitempty"`
}
