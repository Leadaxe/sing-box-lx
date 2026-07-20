package v2rayxhttp

import (
	"context"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

// newPathClient builds a Client wired for the default path-placement layout, as
// produced by NewClient with no placement overrides.
func newPathClient() *Client {
	meta, _ := normalizeMeta(metaOptions{}, modePacketUp)
	return &Client{
		scheme:       "https",
		host:         "example.com",
		serverAddr:   M.ParseSocksaddr("example.com:443"),
		path:         "/xhttp",
		paddingRange: intRange{0, 0}, // disable padding so it does not perturb the URL
		meta:         meta,
	}
}

// newHeaderSessionClient builds a Client that carries the session id in a header
// (session_placement=header) and keeps a trailing-slash path — the shape that
// regressed to a 301 when the path's trailing slash was globally trimmed. Because
// the session id never lands in the path here, the base path must reach the wire
// verbatim ("/upload/"), not stripped to "/upload".
func newHeaderSessionClient(path string) *Client {
	meta, _ := normalizeMeta(metaOptions{
		SessionPlacement: placementHeader,
		SeqPlacement:     placementQuery,
	}, modePacketUp)
	return &Client{
		scheme:       "https",
		host:         "example.com",
		serverAddr:   M.ParseSocksaddr("example.com:443"),
		path:         path,
		paddingRange: intRange{0, 0},
		meta:         meta,
	}
}

// TestRequestURLPaths locks the default (path-placement) URL layout. The stream-one
// fix is the empty-sessionId case: it must yield "<path>" with NO trailing slash and
// NO sessionId, because Xray's splithttp server routes to the bidirectional
// stream-one handler only on an empty sessionId. stream-up/packet-up keep the
// sessionId (and seq) path segments, in that order.
func TestRequestURLPaths(t *testing.T) {
	c := newPathClient()

	cases := []struct {
		name      string
		sessionID string
		seqStr    string
		want      string
	}{
		{"stream-one bare path (no sessionId)", "", "", "/xhttp"},
		{"stream-up/packet-up download (sessionId)", "sid123", "", "/xhttp/sid123"},
		{"packet-up upload (sessionId + seq)", "sid123", "7", "/xhttp/sid123/7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.newRequest(context.Background(), "GET", tc.sessionID, tc.seqStr, nil)
			if err != nil {
				t.Fatalf("newRequest(%q,%q) error: %v", tc.sessionID, tc.seqStr, err)
			}
			if req.URL.Path != tc.want {
				t.Fatalf("newRequest(%q,%q).URL.Path = %q, want %q", tc.sessionID, tc.seqStr, req.URL.Path, tc.want)
			}
		})
	}
}

// TestTrailingSlashPreservedOffPath locks the SPEC 002 trailing-slash fix: when the
// session id is NOT placed in the path (header/query/cookie placement), the base
// path must reach the wire exactly as configured, trailing slash included. A bare
// "/upload" makes an nginx `location /upload/ {}` reply 301, and our download
// RoundTrip does not follow redirects, so the slash must survive. stream-one (empty
// sessionId) is the sole exception: its bare path is trimmed regardless.
func TestTrailingSlashPreservedOffPath(t *testing.T) {
	c := newHeaderSessionClient("/upload/")

	cases := []struct {
		name      string
		sessionID string
		seqStr    string
		want      string
	}{
		{"download keeps trailing slash (session in header)", "sid123", "", "/upload/"},
		{"upload keeps trailing slash (session in header, seq in query)", "sid123", "7", "/upload/"},
		{"stream-one still trims to bare path", "", "", "/upload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.newRequest(context.Background(), "GET", tc.sessionID, tc.seqStr, nil)
			if err != nil {
				t.Fatalf("newRequest(%q,%q) error: %v", tc.sessionID, tc.seqStr, err)
			}
			if req.URL.Path != tc.want {
				t.Fatalf("newRequest(%q,%q).URL.Path = %q, want %q", tc.sessionID, tc.seqStr, req.URL.Path, tc.want)
			}
		})
	}
}
