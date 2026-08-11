package option

import "github.com/sagernet/sing/common/json/badoption"

// V2RayXHTTPOptions configures the XHTTP (Xray "splithttp"/"xhttp") client
// transport. It is a sing-box-lx downstream addition; see SPECS/TASKS/002. JSON keys
// are snake_case to match Xray's stream settings, sing-box-extended and the rest
// of sing-box. See SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/PARAM_MAP.md for the precise
// per-field semantics audited against Xray-core and sing-box-extended.
//
// Modes (Xray-compatible):
//   - "auto"       : Reality → stream-one, otherwise → packet-up.
//   - "packet-up"  : separate GET download stream + sequential POST upload packets.
//   - "stream-up"  : single streamed POST upload + separate GET download stream.
//   - "stream-one" : a single bidirectional HTTP stream (request body up,
//     response body down) — the closest analogue to httpupgrade.
//
// Range fields (uplink_chunk_size, sc_*) are expressed Xray-style as the string
// "min-max" (e.g. "3000-4000") or a single integer ("4000" == "4000-4000"); empty
// selects the documented default. This mirrors x_padding_bytes and avoids adding a
// range type to the option layer.
type V2RayXHTTPOptions struct {
	// ---- v1 fields (unchanged, preserved verbatim) --------------------------

	// Host overrides the HTTP Host header (defaults to the TLS SNI or the
	// server address when empty).
	Host string `json:"host,omitempty"`
	// Path is the request path prefix; the session id (and, for packet-up,
	// the upload sequence number) are appended to it (placement permitting).
	Path string `json:"path,omitempty"`
	// Mode selects the XHTTP transport mode: auto|packet-up|stream-up|stream-one.
	Mode string `json:"mode,omitempty"`
	// Headers are extra request headers sent on every XHTTP request.
	Headers badoption.HTTPHeader `json:"headers,omitempty"`
	// XPaddingBytes is the inclusive byte-length range of the padding value
	// ("min-max", e.g. "100-1000", or a single integer). Empty defaults to
	// "100-1000".
	XPaddingBytes string `json:"x_padding_bytes,omitempty"`
	// Xmux configures HTTP connection reuse (Xray "XMUX"). A nil value still
	// enables XMUX with Xray-compatible defaults — matching Xray-core and
	// sing-box-extended, where the pool is always on. See SPECS/TASKS/059.
	Xmux *V2RayXHTTPXmuxOptions `json:"xmux,omitempty"`

	// NoGRPCHeader omits the "Content-Type: application/grpc" request header that
	// the streamed-body modes (stream-one, stream-up) set by default, mirroring
	// Xray's FillStreamRequest (transport/internet/splithttp/config.go). The
	// header is load-bearing in front of reverse proxies/CDNs, which key
	// response streaming (no buffering) on a gRPC content type — without it a
	// stream-one dial can hang until timeout.
	NoGRPCHeader bool `json:"no_grpc_header,omitempty"`

	// ---- session / seq placement (core) -------------------------------------

	// SessionPlacement selects where the session id is carried on every request:
	// path|query|header|cookie. Empty defaults to "path". (Xray proto field is
	// sessionIDPlacement; we expose session_placement to match sing-box-extended.)
	SessionPlacement string `json:"session_placement,omitempty"`
	// SessionKey is the query param / header / cookie name carrying the session id
	// when placement is not "path". Empty defaults to "X-Session" (header) or
	// "x_session" (query/cookie).
	SessionKey string `json:"session_key,omitempty"`
	// SeqPlacement selects where the per-packet upload sequence number is carried
	// in packet-up mode: path|query|header|cookie. Empty defaults to "path".
	SeqPlacement string `json:"seq_placement,omitempty"`
	// SeqKey is the name carrying the seq when placement is not "path". Empty
	// defaults to "X-Seq" (header) or "x_seq" (query/cookie).
	SeqKey string `json:"seq_key,omitempty"`

	// ---- uplink data (obfs / tuning) ----------------------------------------

	// UplinkDataPlacement selects where the packet-up upload payload is carried:
	// body|auto|header|cookie. Empty defaults to "auto" (client-equivalent to
	// body). header/cookie are only valid in packet-up mode.
	UplinkDataPlacement string `json:"uplink_data_placement,omitempty"`
	// UplinkDataKey is the base header/cookie name for chunked header/cookie
	// payload. Empty defaults to "X-Data" (header/auto) or "x_data" (cookie).
	UplinkDataKey string `json:"uplink_data_key,omitempty"`
	// UplinkChunkSize is the "min-max" range (in base64 characters) of each
	// header/cookie payload chunk. Empty selects a placement-dependent default
	// (cookie 2048-3072, header 3000-4000, otherwise sc_max_each_post_bytes).
	UplinkChunkSize string `json:"uplink_chunk_size,omitempty"`
	// UplinkHTTPMethod is the HTTP method for upload requests (download is always
	// GET). Empty defaults to "POST"; the value is upper-cased. "GET" is only
	// valid in packet-up mode.
	UplinkHTTPMethod string `json:"uplink_http_method,omitempty"`

	// ---- X-Padding obfs ------------------------------------------------------

	// XPaddingObfsMode is the master switch: false (default) carries padding as
	// "x_padding=<pad>" in a Referer header query (legacy Xray); true uses the
	// configurable x_padding_* fields below.
	XPaddingObfsMode bool `json:"x_padding_obfs_mode,omitempty"`
	// XPaddingKey is the cookie/query parameter name for padding in obfs mode.
	// Empty defaults to "x_padding". Unused when placement is "header".
	XPaddingKey string `json:"x_padding_key,omitempty"`
	// XPaddingHeader is the header name for padding in obfs header/queryInHeader
	// placement. Empty defaults to "X-Padding".
	XPaddingHeader string `json:"x_padding_header,omitempty"`
	// XPaddingPlacement selects where padding is placed in obfs mode:
	// cookie|header|query|queryInHeader. Empty defaults to "queryInHeader".
	XPaddingPlacement string `json:"x_padding_placement,omitempty"`
	// XPaddingMethod selects the padding byte generator: repeat-x|tokenish.
	// Empty defaults to "repeat-x".
	XPaddingMethod string `json:"x_padding_method,omitempty"`

	// ---- packet-up tuning (client-relevant extras) --------------------------

	// ScMaxEachPostBytes is the "min-max" range bounding the size of a single
	// packet-up upload POST (the split threshold). Empty defaults to
	// "1000000-1000000".
	ScMaxEachPostBytes string `json:"sc_max_each_post_bytes,omitempty"`
	// ScMinPostsIntervalMs is the "min-max" range (milliseconds) of the minimum
	// delay between successive packet-up upload POSTs (anti-burst). Empty defaults
	// to "30-30".
	ScMinPostsIntervalMs string `json:"sc_min_posts_interval_ms,omitempty"`

	// ---- legacy / accepted-but-ignored -------------------------------------

	// ScMaxConcurrentPosts is a legacy Xray knob (removed from current Xray-core
	// and sing-box-extended) that once capped concurrent upload POSTs. Current
	// Xray serializes to one POST body in flight at a time (matching our
	// sequential packet-up upload), so this field is accepted for config/link
	// symmetry but IGNORED. See SPECS/TASKS/002 PARAM_MAP.md.
	ScMaxConcurrentPosts int `json:"sc_max_concurrent_posts,omitempty"`

	// ---- server-only (accepted for config symmetry, IGNORED by the client) --

	// ServerMaxHeaderBytes is server-only (http.Server.MaxHeaderBytes). Ignored
	// by the client-only transport; accepted so inbound-shaped configs do not error.
	ServerMaxHeaderBytes int `json:"server_max_header_bytes,omitempty"`
	// NoSSEHeader is server-only (omit Content-Type: text/event-stream on the
	// stream-down response). Ignored by the client (it never inspects Content-Type).
	NoSSEHeader bool `json:"no_sse_header,omitempty"`
	// ScMaxBufferedPosts is server-only (upload reorder buffer depth). Ignored by
	// the client.
	ScMaxBufferedPosts int64 `json:"sc_max_buffered_posts,omitempty"`
	// ScStreamUpServerSecs is server-only ("min-max" keepalive-padding interval on
	// the stream-up response). The client sets no such interval and does not strip
	// server-injected keepalive padding from the stream-up download — verify against
	// the target server if it emits any.
	ScStreamUpServerSecs string `json:"sc_stream_up_server_secs,omitempty"`
}

// V2RayXHTTPXmuxOptions configures XHTTP connection reuse ("XMUX"): how many
// HTTP connections the client keeps, how many streams share one, and when a
// connection is rotated out of the pool. Mirrors Xray's XmuxConfig
// (transport/internet/splithttp/config.go) and sing-box-extended's
// V2RayXHTTPXmuxOptions. See SPECS/TASKS/059-XHTTP_XMUX.
//
// The range fields take the "min-max" string form used by the rest of our XHTTP
// options (e.g. "600-900"), a single integer ("4" == "4-4"), or — for configs
// authored against Xray/sing-box-extended — a two-element JSON array ([600,900]).
// Each range is rolled ONCE, not per request: the manager rolls max_concurrency
// and max_connections at construction, and every connection rolls its own reuse
// limits when it is created.
type V2RayXHTTPXmuxOptions struct {
	// MaxConcurrency bounds how many streams may share a single HTTP connection.
	// Empty defaults to "1-1" (one stream per connection, like Xray). Mutually
	// exclusive with MaxConnections.
	MaxConcurrency XmuxRange `json:"max_concurrency,omitempty"`
	// MaxConnections bounds how many connections the pool holds; while the pool
	// is below this count a new connection is always opened. Empty means
	// unlimited. Mutually exclusive with MaxConcurrency.
	MaxConnections XmuxRange `json:"max_connections,omitempty"`
	// CMaxReuseTimes is how many times a connection may be handed out for a new
	// stream before it is retired. Empty means unlimited.
	CMaxReuseTimes XmuxRange `json:"c_max_reuse_times,omitempty"`
	// HMaxRequestTimes is how many HTTP requests may traverse a connection before
	// it is retired. Empty defaults to "600-900". Note this counts requests, not
	// streams: in packet-up one stream issues many upload POSTs.
	HMaxRequestTimes XmuxRange `json:"h_max_request_times,omitempty"`
	// HMaxReusableSecs is how long a connection stays reusable, in seconds. Empty
	// defaults to "1800-3000".
	HMaxReusableSecs XmuxRange `json:"h_max_reusable_secs,omitempty"`
	// HKeepAlivePeriod is the HTTP/2 keep-alive ping period in seconds (the
	// transport's ReadIdleTimeout). Zero selects the default; a negative value
	// disables keep-alive pings. Unlike the fields above this is a plain integer,
	// matching the reference.
	HKeepAlivePeriod int64 `json:"h_keep_alive_period,omitempty"`
}
