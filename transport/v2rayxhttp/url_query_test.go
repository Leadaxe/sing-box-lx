package v2rayxhttp

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// newQueryClient builds a Client through NewClient, so the path/query split of
// the configured path is the one that ships (lx: SPEC 119).
func newQueryClient(t *testing.T, options option.V2RayXHTTPOptions) *Client {
	t.Helper()
	options.XPaddingBytes = "0" // keep padding off the URL under test
	transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("example.com:443"), options, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client := transport.(*Client)
	t.Cleanup(func() { client.Close() })
	return client
}

// TestPathQueryPreserved is the regression guard for issue #36: a query in the
// configured path is the request query, as in Xray (GetNormalizedPath /
// GetNormalizedQuery). Kept inside the path it went on the wire as "%3F…", and a
// Cloudflare-Worker relay reading proxyip from the query got nothing.
func TestPathQueryPreserved(t *testing.T) {
	cases := []struct {
		name      string
		options   option.V2RayXHTTPOptions
		sessionID string
		seqStr    string
		wantPath  string
		wantQuery map[string]string
	}{
		{
			name:      "stream-one root path with query",
			options:   option.V2RayXHTTPOptions{Mode: modeStreamOne, Path: "/?proxyip=149.56.109.62"},
			wantPath:  "/",
			wantQuery: map[string]string{"proxyip": "149.56.109.62"},
		},
		{
			name:      "packet-up upload, session and seq in path",
			options:   option.V2RayXHTTPOptions{Mode: modePacketUp, Path: "/base?x=1"},
			sessionID: "sid123",
			seqStr:    "7",
			wantPath:  "/base/sid123/7",
			wantQuery: map[string]string{"x": "1"},
		},
		{
			name: "packet-up upload, session and seq in query",
			options: option.V2RayXHTTPOptions{
				Mode:             modePacketUp,
				Path:             "/base?x=1",
				SessionPlacement: placementQuery,
				SeqPlacement:     placementQuery,
			},
			sessionID: "sid123",
			seqStr:    "7",
			wantPath:  "/base",
			wantQuery: map[string]string{"x": "1", "x_session": "sid123", "x_seq": "7"},
		},
		{
			name:      "path without query is unchanged",
			options:   option.V2RayXHTTPOptions{Mode: modePacketUp, Path: "/xhttp"},
			sessionID: "sid123",
			seqStr:    "7",
			wantPath:  "/xhttp/sid123/7",
			wantQuery: map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newQueryClient(t, tc.options)
			req, err := c.newRequest(context.Background(), "POST", tc.sessionID, tc.seqStr, nil)
			if err != nil {
				t.Fatalf("newRequest: %v", err)
			}
			if req.URL.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", req.URL.Path, tc.wantPath)
			}
			query := req.URL.Query()
			if len(query) != len(tc.wantQuery) {
				t.Fatalf("query = %q, want exactly %v", req.URL.RawQuery, tc.wantQuery)
			}
			for key, value := range tc.wantQuery {
				if got := query.Get(key); got != value {
					t.Fatalf("query %q = %q, want %q (raw %q)", key, got, value, req.URL.RawQuery)
				}
			}
		})
	}
}

// TestPathQueryWireForm locks the exact request target for the issue #36 config:
// the query goes out verbatim, not percent-encoded into the path.
func TestPathQueryWireForm(t *testing.T) {
	c := newQueryClient(t, option.V2RayXHTTPOptions{Mode: modeStreamOne, Path: "/?proxyip=149.56.109.62"})
	req, err := c.newRequest(context.Background(), "POST", "", "", nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	if got, want := req.URL.RequestURI(), "/?proxyip=149.56.109.62"; got != want {
		t.Fatalf("RequestURI() = %q, want %q", got, want)
	}
}

// TestPathQueryKeptByQueryPadding checks that obfs padding placed in the query is
// added next to the configured query, not instead of it.
func TestPathQueryKeptByQueryPadding(t *testing.T) {
	c := newQueryClient(t, option.V2RayXHTTPOptions{
		Mode:              modeStreamOne,
		Path:              "/base?x=1",
		XPaddingObfsMode:  true,
		XPaddingPlacement: placementQuery,
	})
	c.paddingRange = intRange{16, 16}
	req, err := c.newRequest(context.Background(), "POST", "", "", nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	query := req.URL.Query()
	if query.Get("x") != "1" {
		t.Fatalf("configured query lost: %q", req.URL.RawQuery)
	}
	if len(query.Get(c.meta.xPaddingKey)) != 16 {
		t.Fatalf("query padding missing: %q", req.URL.RawQuery)
	}
}
