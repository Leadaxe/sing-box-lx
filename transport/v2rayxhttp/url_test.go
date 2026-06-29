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
