package v2rayxhttp

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/log"

	E "github.com/sagernet/sing/common/exceptions"
)

// Placement constants (Xray-compatible). They name WHERE a metadata value
// (session id, sequence number, uplink payload, padding) is carried on a request.
const (
	placementPath          = "path"
	placementQuery         = "query"
	placementHeader        = "header"
	placementCookie        = "cookie"
	placementBody          = "body"
	placementAuto          = "auto"
	placementQueryInHeader = "queryInHeader"
)

// Padding generator methods (Xray-compatible).
const (
	methodRepeatX  = "repeat-x"
	methodTokenish = "tokenish"
)

// intRange is a parsed inclusive [min,max] integer range. Range option fields are
// expressed as the "min-max" string form (see parsePaddingRange / parseRange).
type intRange struct {
	min int
	max int
}

// rand returns a random value in [min,max]. With min==max it returns min.
func (r intRange) rand() int {
	if r.max <= r.min {
		return r.min
	}
	return r.min + randIntn(r.max-r.min+1)
}

// metaConfig holds the normalized, validated placement/key/method selection for a
// client. It is computed once in NewClient from V2RayXHTTPOptions and consulted on
// every request, so per-request work stays allocation-light.
type metaConfig struct {
	sessionPlacement string
	sessionKey       string
	seqPlacement     string
	seqKey           string

	uplinkDataPlacement string
	uplinkDataKey       string
	uplinkChunkSize     intRange // resolved (placement-dependent default already applied)
	uplinkHTTPMethod    string

	xPaddingObfsMode  bool
	xPaddingKey       string
	xPaddingHeader    string
	xPaddingPlacement string
	xPaddingMethod    string

	scMaxEachPostBytes   intRange
	scMinPostsIntervalMs intRange
}

// normalizeMeta validates the placement/obfs option set against the selected mode
// and resolves all defaults, mirroring sing-box-extended checkV2RayXHTTPBaseOptions
// + GetNormalized*. See SPECS/TASKS/002 PARAM_MAP.md for the per-field rules.
func normalizeMeta(opts metaOptions, mode string) (metaConfig, error) {
	var m metaConfig

	// --- session placement / key ---
	m.sessionPlacement = orDefault(opts.SessionPlacement, placementPath)
	if err := validatePlacement("session_placement", m.sessionPlacement, placementPath, placementQuery, placementHeader, placementCookie); err != nil {
		return m, err
	}
	m.sessionKey = resolveKey(opts.SessionKey, m.sessionPlacement, "X-Session", "x_session")

	// --- seq placement / key ---
	m.seqPlacement = orDefault(opts.SeqPlacement, placementPath)
	if err := validatePlacement("seq_placement", m.seqPlacement, placementPath, placementQuery, placementHeader, placementCookie); err != nil {
		return m, err
	}
	m.seqKey = resolveKey(opts.SeqKey, m.seqPlacement, "X-Seq", "x_seq")

	// --- uplink data placement / key ---
	m.uplinkDataPlacement = orDefault(opts.UplinkDataPlacement, placementAuto)
	if err := validatePlacement("uplink_data_placement", m.uplinkDataPlacement, placementBody, placementAuto, placementHeader, placementCookie); err != nil {
		return m, err
	}
	// header/cookie payload placement is only meaningful for packet-up.
	if (m.uplinkDataPlacement == placementHeader || m.uplinkDataPlacement == placementCookie) && mode != modePacketUp {
		return m, E.New("v2ray-xhttp: uplink_data_placement can be ", m.uplinkDataPlacement, " only in packet-up mode")
	}
	m.uplinkDataKey = resolveUplinkDataKey(opts.UplinkDataKey, m.uplinkDataPlacement)

	// --- uplink http method ---
	m.uplinkHTTPMethod = strings.ToUpper(orDefault(opts.UplinkHTTPMethod, http.MethodPost))
	if m.uplinkHTTPMethod == http.MethodGet && mode != modePacketUp {
		// GET can only carry the uplink in packet-up (other modes put the uplink in
		// the request body, which GET cannot have). A subscription node sometimes
		// ships method=GET on a non-packet-up node; rather than fail the WHOLE config
		// over one bad outbound, fall back to POST (the safe default that works in
		// every mode) and warn, so the rest of the config still loads. lx: SPEC 002.
		log.StdLogger().Warn("v2ray-xhttp: uplink_http_method=GET is only valid in packet-up mode (mode=", mode, "); falling back to POST")
		m.uplinkHTTPMethod = http.MethodPost
	}

	// --- packet-up tuning ranges ---
	var err error
	if m.scMaxEachPostBytes, err = parseRangeOr(opts.ScMaxEachPostBytes, "sc_max_each_post_bytes", intRange{1000000, 1000000}); err != nil {
		return m, err
	}
	if m.scMinPostsIntervalMs, err = parseRangeOr(opts.ScMinPostsIntervalMs, "sc_min_posts_interval_ms", intRange{30, 30}); err != nil {
		return m, err
	}

	// --- uplink chunk size (placement-dependent default) ---
	m.uplinkChunkSize, err = resolveUplinkChunkSize(opts.UplinkChunkSize, m.uplinkDataPlacement, m.scMaxEachPostBytes)
	if err != nil {
		return m, err
	}

	// --- X-Padding obfs ---
	m.xPaddingObfsMode = opts.XPaddingObfsMode
	m.xPaddingKey = orDefault(opts.XPaddingKey, "x_padding")
	m.xPaddingHeader = orDefault(opts.XPaddingHeader, "X-Padding")
	m.xPaddingPlacement = orDefault(opts.XPaddingPlacement, placementQueryInHeader)
	if err := validatePlacement("x_padding_placement", m.xPaddingPlacement, placementCookie, placementHeader, placementQuery, placementQueryInHeader); err != nil {
		return m, err
	}
	m.xPaddingMethod = orDefault(opts.XPaddingMethod, methodRepeatX)
	switch m.xPaddingMethod {
	case methodRepeatX, methodTokenish:
	default:
		return m, E.New("v2ray-xhttp: unknown x_padding_method: ", m.xPaddingMethod)
	}

	return m, nil
}

// metaOptions is the subset of V2RayXHTTPOptions consumed by normalizeMeta. Keeping it
// as a thin local struct lets normalizeMeta be tested without importing the option package.
type metaOptions struct {
	SessionPlacement     string
	SessionKey           string
	SeqPlacement         string
	SeqKey               string
	UplinkDataPlacement  string
	UplinkDataKey        string
	UplinkChunkSize      string
	UplinkHTTPMethod     string
	XPaddingObfsMode     bool
	XPaddingKey          string
	XPaddingHeader       string
	XPaddingPlacement    string
	XPaddingMethod       string
	ScMaxEachPostBytes   string
	ScMinPostsIntervalMs string
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func validatePlacement(field, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return E.New("v2ray-xhttp: unsupported ", field, ": ", value)
}

// resolveKey returns the configured key, or the header/other default per placement.
// For path placement the key is unused and resolves to "".
func resolveKey(configured, placement, headerDefault, otherDefault string) string {
	if configured != "" {
		return configured
	}
	switch placement {
	case placementHeader:
		return headerDefault
	case placementQuery, placementCookie:
		return otherDefault
	default: // path
		return ""
	}
}

// resolveUplinkDataKey mirrors checkV2RayXHTTPBaseOptions: X-Data for header/auto,
// x_data for cookie, empty for body.
func resolveUplinkDataKey(configured, placement string) string {
	if configured != "" {
		return configured
	}
	switch placement {
	case placementHeader, placementAuto:
		return "X-Data"
	case placementCookie:
		return "x_data"
	default: // body
		return ""
	}
}

// resolveUplinkChunkSize mirrors GetNormalizedUplinkChunkSize: placement-dependent
// default and a floor of 64 base64 characters.
func resolveUplinkChunkSize(raw, placement string, scMaxEachPost intRange) (intRange, error) {
	if strings.TrimSpace(raw) != "" {
		r, err := parseRange(raw, "uplink_chunk_size")
		if err != nil {
			return intRange{}, err
		}
		return clampChunkFloor(r), nil
	}
	switch placement {
	case placementCookie:
		return intRange{2048, 3072}, nil
	case placementHeader:
		return intRange{3000, 4000}, nil
	default:
		return scMaxEachPost, nil
	}
}

func clampChunkFloor(r intRange) intRange {
	if r.min < 64 {
		r.min = 64
	}
	if r.max < r.min {
		r.max = r.min
	}
	return r
}

// parseRangeOr parses a "min-max" range or returns def when raw is empty.
func parseRangeOr(raw, field string, def intRange) (intRange, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	return parseRange(raw, field)
}

// parseRange parses "min-max" or a single integer "n" (== "n-n").
func parseRange(raw, field string) (intRange, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "-") {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return intRange{}, E.Cause(err, "parse "+field)
		}
		return intRange{v, v}, nil
	}
	parts := strings.SplitN(raw, "-", 2)
	minV, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return intRange{}, E.Cause(err, "parse "+field+" min")
	}
	maxV, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return intRange{}, E.Cause(err, "parse "+field+" max")
	}
	if maxV < minV {
		minV, maxV = maxV, minV
	}
	return intRange{minV, maxV}, nil
}

// applyMeta writes the session id and (for packet-up) the sequence number onto the
// request per the configured placements. The base path has already been set on
// u.Path by the caller. For path placement, session id is the FIRST appended
// segment and seq the SECOND — the order is load-bearing (the server reads path
// segments positionally). seqStr == "" means "no seq" (stream modes).
//
// applyMeta mutates the request URL (path/query) and headers/cookies in place; the
// final path is written to request.URL.Path.
func (c *Client) applyMeta(request *http.Request, basePath, sessionID, seqStr string) {
	m := &c.meta
	path := basePath

	// session id — an empty sessionID (stream-one) emits no session metadata, but
	// the path still carries the trailing slash that path-placement implies.
	// Xray/NekoBox normalize the CONFIGURED path to end in "/" whenever session or
	// seq live in the path (GetNormalizedPath), and the server prefix-matches every
	// request against that normalized path. Trimming the slash here made stream-one
	// request "<path>" against a server expecting "<path>/" — no prefix match, 404,
	// and the dial hung until timeout (lx: SPEC 043, wire-reproduced).
	if sessionID == "" {
		path = barePathForStreamOne(path, m)
	} else {
		switch m.sessionPlacement {
		case placementPath:
			path = appendPathSegment(path, sessionID)
		case placementQuery:
			setQuery(request.URL, m.sessionKey, sessionID)
		case placementHeader:
			request.Header.Set(m.sessionKey, sessionID)
		case placementCookie:
			request.AddCookie(&http.Cookie{Name: m.sessionKey, Value: sessionID, Path: "/"})
		}
	}

	// seq (packet-up only)
	if seqStr != "" {
		switch m.seqPlacement {
		case placementPath:
			path = appendPathSegment(path, seqStr)
		case placementQuery:
			setQuery(request.URL, m.seqKey, seqStr)
		case placementHeader:
			request.Header.Set(m.seqKey, seqStr)
		case placementCookie:
			request.AddCookie(&http.Cookie{Name: m.seqKey, Value: seqStr, Path: "/"})
		}
	}

	request.URL.Path = path
}

// applyUplinkData attaches a packet-up upload payload to a request per
// uplink_data_placement:
//   - body / auto: the raw payload is the request body (Content-Length set,
//     Content-Type application/octet-stream). On the client "auto" == body.
//   - header / cookie: the payload is base64.RawURLEncoding-encoded and sliced into
//     chunks sized by uplink_chunk_size; each chunk i becomes header "<key>-<i>" or
//     cookie "<key>_<i>" (i ascending from 0). No request body.
func (c *Client) applyUplinkData(request *http.Request, payload []byte) {
	m := &c.meta
	switch m.uplinkDataPlacement {
	case placementHeader:
		for i, chunk := range chunkEncoded(payload, m.uplinkChunkSize) {
			request.Header.Set(fmt.Sprintf("%s-%d", m.uplinkDataKey, i), chunk)
		}
	case placementCookie:
		for i, chunk := range chunkEncoded(payload, m.uplinkChunkSize) {
			request.AddCookie(&http.Cookie{Name: fmt.Sprintf("%s_%d", m.uplinkDataKey, i), Value: chunk, Path: "/"})
		}
	default: // body / auto
		request.Body = readCloser{&byteReader{data: payload}}
		request.Header.Set("Content-Type", "application/octet-stream")
		request.ContentLength = int64(len(payload))
	}
}

// chunkEncoded base64.RawURLEncoding-encodes the payload and splits the encoded
// string into chunks. Each chunk takes a random size in the configured range,
// capped by the remaining length; the server re-joins chunks blindly so any valid
// split works.
func chunkEncoded(payload []byte, size intRange) []string {
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	var chunks []string
	for len(encoded) > 0 {
		n := size.rand()
		if n <= 0 || n > len(encoded) {
			n = len(encoded)
		}
		chunks = append(chunks, encoded[:n])
		encoded = encoded[n:]
	}
	return chunks
}

// timeNow is a small indirection over time.Now to keep the throttle logic testable.
func timeNow() time.Time { return time.Now() }

// trimBarePathSlash strips trailing slashes for the stream-one bare-path case,
// while never collapsing the root path to "" (a root-only path stays "/"). Used
// only when no sessionId is appended, so it cannot affect proxy routing for the
// other modes, which keep the configured path verbatim.
// barePathForStreamOne returns the path a stream-one request targets. stream-one
// sends no sessionId, but the trailing slash is still load-bearing: Xray and
// NekoBox normalize the configured path to end in "/" when session or seq are
// placed in the path, and the server prefix-matches requests against it. So the
// slash is appended under exactly the same condition, and left alone otherwise
// (non-path placements keep the configured path verbatim).
func barePathForStreamOne(path string, m *metaConfig) string {
	if path == "" {
		return "/"
	}
	if m.sessionPlacement != placementPath && m.seqPlacement != placementPath {
		return path
	}
	if !strings.HasSuffix(path, "/") {
		return path + "/"
	}
	return path
}

// appendPathSegment joins a "/"-separated segment onto a path, inserting exactly one
// separator. Mirrors Xray's appendToPath.
func appendPathSegment(path, seg string) string {
	if strings.HasSuffix(path, "/") {
		return path + seg
	}
	return path + "/" + seg
}

// setQuery adds key=value to the URL's RawQuery, preserving any existing params.
func setQuery(u *url.URL, key, value string) {
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
}
