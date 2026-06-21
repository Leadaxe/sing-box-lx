package v2rayxhttp

import (
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

// TestRequestURLPaths locks the path layout per mode. The stream-one fix is the
// bare-path (zero-elem) case: it must yield "<path>" with NO trailing slash and
// NO sessionId, because Xray's splithttp server routes to the bidirectional
// stream-one handler only on an empty sessionId. stream-up/packet-up keep the
// sessionId (and seq) segments.
func TestRequestURLPaths(t *testing.T) {
	c := &Client{scheme: "https", serverAddr: M.ParseSocksaddr("example.com:443"), path: "/xhttp"}

	cases := []struct {
		name string
		elem []string
		want string
	}{
		{"stream-one bare path (no sessionId)", nil, "/xhttp"},
		{"stream-up/packet-up download (sessionId)", []string{"sid123"}, "/xhttp/sid123"},
		{"packet-up upload (sessionId + seq)", []string{"sid123", "7"}, "/xhttp/sid123/7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := c.requestURL(tc.elem...)
			if err != nil {
				t.Fatalf("requestURL(%v) error: %v", tc.elem, err)
			}
			if u.Path != tc.want {
				t.Fatalf("requestURL(%v).Path = %q, want %q", tc.elem, u.Path, tc.want)
			}
		})
	}
}
