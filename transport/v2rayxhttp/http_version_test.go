package v2rayxhttp

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func testTLSConfig(t *testing.T, alpn ...string) tls.Config {
	t.Helper()
	config, err := tls.NewClient(context.Background(), log.NewNOPFactory().Logger(), "example.com", option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "example.com",
		ALPN:       alpn,
	})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// TestDecideHTTPVersion walks SPEC 104 §5.1 row by row (Xray decideHTTPVersion).
func TestDecideHTTPVersion(t *testing.T) {
	cases := []struct {
		name    string
		noTLS   bool
		reality bool
		alpn    []string
		want    httpVersion
	}{
		{name: "no TLS", noTLS: true, want: httpVersion11},
		{name: "REALITY, empty alpn", reality: true, want: httpVersion2},
		{name: "REALITY, h3", reality: true, alpn: []string{"h3"}, want: httpVersion2},
		{name: "REALITY, http/1.1", reality: true, alpn: []string{"http/1.1"}, want: httpVersion2},
		{name: "TLS, empty alpn", want: httpVersion2},
		{name: "TLS, two entries", alpn: []string{"h3", "h2"}, want: httpVersion2},
		{name: "TLS, h2 + http/1.1", alpn: []string{"h2", "http/1.1"}, want: httpVersion2},
		{name: "TLS, http/1.1", alpn: []string{"http/1.1"}, want: httpVersion11},
		{name: "TLS, h3", alpn: []string{"h3"}, want: httpVersion3},
		{name: "TLS, h2", alpn: []string{"h2"}, want: httpVersion2},
		{name: "TLS, unknown single entry", alpn: []string{"h3-29"}, want: httpVersion2},
		{name: "TLS, case differs", alpn: []string{"H3"}, want: httpVersion2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var config tls.Config
			if !testCase.noTLS {
				config = testTLSConfig(t, testCase.alpn...)
			}
			if got := decideHTTPVersion(config, testCase.reality); got != testCase.want {
				t.Fatalf("decideHTTPVersion = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestRealityALPNNeedsH2(t *testing.T) {
	cases := []struct {
		alpn []string
		want bool
	}{
		{nil, false},
		{[]string{"h2"}, false},
		{[]string{"h2", "http/1.1"}, false},
		{[]string{"h3"}, true},
		{[]string{"http/1.1"}, true},
	}
	for _, testCase := range cases {
		if got := realityALPNNeedsH2(testCase.alpn); got != testCase.want {
			t.Fatalf("realityALPNNeedsH2(%v) = %v, want %v", testCase.alpn, got, testCase.want)
		}
	}
}

// TestNewClientPoolFollowsVersion: the pool hands out the connection kind the
// version rule picked; cleartext is HTTP/1.1 now, not h2c.
func TestNewClientPoolFollowsVersion(t *testing.T) {
	cases := []struct {
		name   string
		config tls.Config
		scheme string
		check  func(xmuxConn) bool
	}{
		{"no TLS", nil, "http", func(c xmuxConn) bool { _, ok := c.(*http1XmuxConn); return ok }},
		{"TLS http/1.1", testTLSConfig(t, "http/1.1"), "https", func(c xmuxConn) bool { _, ok := c.(*http1XmuxConn); return ok }},
		{"TLS default", testTLSConfig(t), "https", func(c xmuxConn) bool { _, ok := c.(*http2XmuxConn); return ok }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), option.V2RayXHTTPOptions{}, testCase.config)
			if err != nil {
				t.Fatal(err)
			}
			client := transport.(*Client)
			defer client.Close()
			if client.scheme != testCase.scheme {
				t.Fatalf("scheme = %q, want %q", client.scheme, testCase.scheme)
			}
			if conn := client.xmux.newConn(); !testCase.check(conn) {
				t.Fatalf("pool connection is %T", conn)
			}
		})
	}
	// The default HTTP/2 branch still puts h2 into an empty ALPN.
	config := testTLSConfig(t)
	if _, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), option.V2RayXHTTPOptions{}, config); err != nil {
		t.Fatal(err)
	}
	if nextProtos := config.NextProtos(); len(nextProtos) != 1 || nextProtos[0] != "h2" {
		t.Fatalf("HTTP/2 ALPN = %v, want [h2]", nextProtos)
	}
}
