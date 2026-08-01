package v2rayxhttp

import (
	"context"
	"strings"
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

// TestRequestURLPaths locks the default (path-placement) URL layout. stream-one is
// the empty-sessionId case: NO sessionId on the wire (that is how Xray's server
// picks the bidirectional handler) but WITH the trailing slash that path placement
// implies. stream-up/packet-up keep the sessionId (and seq) path segments, in that
// order.
func TestRequestURLPaths(t *testing.T) {
	c := newPathClient()

	cases := []struct {
		name      string
		sessionID string
		seqStr    string
		want      string
	}{
		// stream-one sends no sessionId, but path placement still implies the
		// trailing slash: the server prefix-matches against its own normalized
		// "<path>/" (Xray/NekoBox GetNormalizedPath), so "/xhttp" would 404.
		{"stream-one bare path (no sessionId)", "", "", "/xhttp/"},
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
// RoundTrip does not follow redirects, so the slash must survive. stream-one is no
// exception: with the session off the path there is nothing to normalize, so the
// configured path reaches the wire verbatim there too.
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
		{"stream-one keeps configured path verbatim (session off path)", "", "", "/upload/"},
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

// TestStreamOnePathPrefixMatchesServer is the regression guard for SPEC 043. An
// XHTTP server (Xray, NekoBox) normalizes its configured path to end in "/" when
// session or seq are path-placed, then serves only requests carrying that prefix.
// stream-one used to trim the slash, so "<path>" missed the server's "<path>/"
// prefix, drew a 404, and the dial hung until timeout — while packet-up, whose
// path continues into "/<sessionId>", matched and worked. Reproduced on the wire
// against a prefix-checking server before the fix.
func TestStreamOnePathPrefixMatchesServer(t *testing.T) {
	serverPath := func(configured string) string { // mirrors GetNormalizedPath
		if !strings.HasSuffix(configured, "/") {
			return configured + "/"
		}
		return configured
	}

	for _, configured := range []string{"/api/v1/feed", "/api/v1/feed/", "/"} {
		t.Run(configured, func(t *testing.T) {
			c := newPathClient()
			c.path = configured

			req, err := c.newRequest(context.Background(), "POST", "", "", nil)
			if err != nil {
				t.Fatalf("newRequest: %v", err)
			}
			want := serverPath(configured)
			if !strings.HasPrefix(req.URL.Path, want) {
				t.Fatalf("stream-one path %q does not match server prefix %q — server would 404", req.URL.Path, want)
			}
			// stream-one carries no session id: the path must be exactly the
			// normalized base, nothing appended.
			if req.URL.Path != want {
				t.Fatalf("stream-one path = %q, want exactly %q (no sessionId)", req.URL.Path, want)
			}
		})
	}
}
