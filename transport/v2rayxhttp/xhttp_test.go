package v2rayxhttp

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2/hpack"
)

// clientWith builds a Client from option-like metaOptions, normalized for the given
// mode, with padding disabled unless padRange is set. It mirrors what NewClient
// produces minus the transport/TLS wiring (not needed for request-shaping tests).
func clientWith(t *testing.T, mode string, padRange intRange, opts metaOptions) *Client {
	t.Helper()
	meta, err := normalizeMeta(opts, mode)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	return &Client{
		scheme:       "https",
		host:         "example.com",
		serverAddr:   M.ParseSocksaddr("example.com:443"),
		path:         "/xhttp",
		mode:         mode,
		paddingRange: padRange,
		meta:         meta,
	}
}

func mustRequest(t *testing.T, c *Client, method, sessionID, seqStr string) *http.Request {
	t.Helper()
	req, err := c.newRequest(context.Background(), method, sessionID, seqStr, nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	return req
}

// --- default (zero-regression) -------------------------------------------------

// TestDefaultLegacyPadding locks the v1 on-wire shape: padding lives in the Referer
// header as an x_padding query param, path carries session/seq, no obfs headers.
func TestDefaultLegacyPadding(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{100, 100}, metaOptions{})
	req := mustRequest(t, c, "POST", "sid", "3")

	if req.URL.Path != "/xhttp/sid/3" {
		t.Fatalf("path = %q, want /xhttp/sid/3", req.URL.Path)
	}
	referer := req.Header.Get("Referer")
	if referer == "" {
		t.Fatal("missing Referer header")
	}
	if !strings.Contains(referer, "x_padding=") {
		t.Fatalf("Referer %q has no x_padding query", referer)
	}
	// padding value must be 100 '0' bytes — the legacy non-obfs filler, kept
	// byte-identical to the live-verified v1 default.
	want := "x_padding=" + strings.Repeat("0", 100)
	if !strings.Contains(referer, want) {
		t.Fatalf("Referer %q missing %q", referer, want)
	}
	// no obfs placement headers should be present.
	if req.Header.Get("X-Padding") != "" {
		t.Fatal("unexpected X-Padding header in non-obfs mode")
	}
}

// --- session / seq placement ---------------------------------------------------

func TestSessionPlacement(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{SessionPlacement: "query"})
		req := mustRequest(t, c, "GET", "sid", "")
		if got := req.URL.Query().Get("x_session"); got != "sid" {
			t.Fatalf("query x_session = %q, want sid", got)
		}
		if req.URL.Path != "/xhttp" {
			t.Fatalf("path = %q, want /xhttp (no path segment for query placement)", req.URL.Path)
		}
	})
	t.Run("header custom key", func(t *testing.T) {
		c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{SessionPlacement: "header", SessionKey: "X-My-Sess"})
		req := mustRequest(t, c, "GET", "sid", "")
		if got := req.Header.Get("X-My-Sess"); got != "sid" {
			t.Fatalf("header X-My-Sess = %q, want sid", got)
		}
	})
	t.Run("cookie default key", func(t *testing.T) {
		c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{SessionPlacement: "cookie"})
		req := mustRequest(t, c, "GET", "sid", "")
		ck, err := req.Cookie("x_session")
		if err != nil || ck.Value != "sid" {
			t.Fatalf("cookie x_session = %v (err %v), want sid", ck, err)
		}
	})
}

func TestSeqPlacement(t *testing.T) {
	t.Run("header default key X-Seq", func(t *testing.T) {
		c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{SeqPlacement: "header"})
		req := mustRequest(t, c, "POST", "sid", "42")
		if got := req.Header.Get("X-Seq"); got != "42" {
			t.Fatalf("header X-Seq = %q, want 42", got)
		}
		// session stays on path (default), seq moved to header.
		if req.URL.Path != "/xhttp/sid" {
			t.Fatalf("path = %q, want /xhttp/sid", req.URL.Path)
		}
	})
	t.Run("query default key x_seq", func(t *testing.T) {
		c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{SeqPlacement: "query"})
		req := mustRequest(t, c, "POST", "sid", "42")
		if got := req.URL.Query().Get("x_seq"); got != "42" {
			t.Fatalf("query x_seq = %q, want 42", got)
		}
	})
}

// --- uplink data placement -----------------------------------------------------

func TestUplinkDataBody(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{}) // default body/auto
	req := mustRequest(t, c, "POST", "sid", "0")
	payload := []byte("hello world")
	c.applyUplinkData(req, payload)
	if req.ContentLength != int64(len(payload)) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(payload))
	}
	body := make([]byte, len(payload))
	n, _ := req.Body.Read(body)
	if string(body[:n]) != string(payload) {
		t.Fatalf("body = %q, want %q", body[:n], payload)
	}
}

func TestUplinkDataHeaderChunks(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{
		UplinkDataPlacement: "header",
		UplinkChunkSize:     "64-64", // small chunks to force multiple headers
	})
	req := mustRequest(t, c, "POST", "sid", "0")
	payload := []byte(strings.Repeat("payload-data ", 30)) // ~390 bytes
	c.applyUplinkData(req, payload)

	// reassemble from X-Data-0, X-Data-1, ... until a gap, mirroring the server.
	var encoded strings.Builder
	for i := 0; ; i++ {
		v := req.Header.Get("X-Data-" + itoa(i))
		if v == "" {
			break
		}
		encoded.WriteString(v)
	}
	got, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		t.Fatalf("decode reassembled payload: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("reassembled = %q, want %q", got, payload)
	}
	if req.Body != nil {
		t.Fatal("header placement must not set a body")
	}
}

func TestUplinkDataCookieChunks(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{0, 0}, metaOptions{
		UplinkDataPlacement: "cookie",
		UplinkChunkSize:     "100-100",
	})
	req := mustRequest(t, c, "POST", "sid", "0")
	payload := []byte(strings.Repeat("abc", 200))
	c.applyUplinkData(req, payload)

	cookieValue := map[string]string{}
	for _, ck := range req.Cookies() {
		cookieValue[ck.Name] = ck.Value
	}
	var encoded strings.Builder
	for i := 0; ; i++ {
		v, ok := cookieValue["x_data_"+itoa(i)]
		if !ok {
			break
		}
		encoded.WriteString(v)
	}
	got, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("reassembled cookie payload mismatch")
	}
}

// --- X-Padding obfs placements -------------------------------------------------

func TestXPaddingObfsPlacements(t *testing.T) {
	const padLen = 120
	base := func(placement string) *Client {
		return clientWith(t, modePacketUp, intRange{padLen, padLen}, metaOptions{
			XPaddingObfsMode:  true,
			XPaddingPlacement: placement,
		})
	}

	t.Run("header", func(t *testing.T) {
		req := mustRequest(t, base("header"), "POST", "sid", "0")
		if got := req.Header.Get("X-Padding"); len(got) != padLen {
			t.Fatalf("X-Padding len = %d, want %d", len(got), padLen)
		}
		if req.Header.Get("Referer") != "" {
			t.Fatal("obfs mode must not set Referer")
		}
	})
	t.Run("cookie", func(t *testing.T) {
		req := mustRequest(t, base("cookie"), "POST", "sid", "0")
		ck, err := req.Cookie("x_padding")
		if err != nil || len(ck.Value) != padLen {
			t.Fatalf("cookie x_padding = %v (err %v), want len %d", ck, err, padLen)
		}
	})
	t.Run("query", func(t *testing.T) {
		req := mustRequest(t, base("query"), "POST", "sid", "0")
		if got := req.URL.Query().Get("x_padding"); len(got) != padLen {
			t.Fatalf("query x_padding len = %d, want %d", len(got), padLen)
		}
	})
	t.Run("queryInHeader", func(t *testing.T) {
		req := mustRequest(t, base("queryInHeader"), "POST", "sid", "0")
		hv := req.Header.Get("X-Padding")
		if !strings.Contains(hv, "x_padding=") {
			t.Fatalf("X-Padding header %q has no x_padding query", hv)
		}
		if !strings.Contains(hv, strings.Repeat("X", padLen)) {
			t.Fatalf("X-Padding header %q missing %d-byte padding", hv, padLen)
		}
	})
}

func TestXPaddingCustomKeyHeader(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{50, 50}, metaOptions{
		XPaddingObfsMode:  true,
		XPaddingPlacement: "header",
		XPaddingHeader:    "X-Custom-Pad",
	})
	req := mustRequest(t, c, "POST", "sid", "0")
	if len(req.Header.Get("X-Custom-Pad")) != 50 {
		t.Fatalf("custom padding header not set correctly")
	}
}

// --- tokenish padding ----------------------------------------------------------

func TestTokenishPaddingHuffmanLength(t *testing.T) {
	for _, target := range []int{64, 100, 256, 1000} {
		pad := generateTokenishPaddingBase62(target)
		// base62-only content (no literal run of identical chars is required, but
		// every byte must be in the alphabet).
		for _, r := range pad {
			if !strings.ContainsRune(base62Alphabet, r) {
				t.Fatalf("tokenish padding contains non-base62 byte %q", r)
			}
		}
		hlen := int(hpack.HuffmanEncodeLength(pad))
		if hlen < target-2 || hlen > target+2 {
			t.Fatalf("target %d: Huffman length %d out of [%d,%d]", target, hlen, target-2, target+2)
		}
	}
}

func TestTokenishUsedWhenConfigured(t *testing.T) {
	c := clientWith(t, modePacketUp, intRange{200, 200}, metaOptions{
		XPaddingObfsMode:  true,
		XPaddingPlacement: "header",
		XPaddingMethod:    "tokenish",
	})
	req := mustRequest(t, c, "POST", "sid", "0")
	v := req.Header.Get("X-Padding")
	if v == "" {
		t.Fatal("no padding emitted")
	}
	if v == strings.Repeat("X", len(v)) {
		t.Fatal("tokenish padding must not be a pure run of X")
	}
	hlen := int(hpack.HuffmanEncodeLength(v))
	if hlen < 198 || hlen > 202 {
		t.Fatalf("tokenish Huffman length %d out of [198,202]", hlen)
	}
}

// --- validation / mode gates ---------------------------------------------------

func TestValidationRejections(t *testing.T) {
	cases := []struct {
		name string
		mode string
		opts metaOptions
	}{
		{"bad session placement", modePacketUp, metaOptions{SessionPlacement: "body"}},
		{"bad seq placement", modePacketUp, metaOptions{SeqPlacement: "nowhere"}},
		{"bad uplink data placement", modePacketUp, metaOptions{UplinkDataPlacement: "path"}},
		{"uplink header outside packet-up", modeStreamOne, metaOptions{UplinkDataPlacement: "header"}},
		{"GET method outside packet-up", modeStreamUp, metaOptions{UplinkHTTPMethod: "GET"}},
		{"bad padding placement", modePacketUp, metaOptions{XPaddingObfsMode: true, XPaddingPlacement: "path"}},
		{"bad padding method", modePacketUp, metaOptions{XPaddingMethod: "rot13"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeMeta(tc.opts, tc.mode); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestValidationAccepts(t *testing.T) {
	// header/cookie uplink and GET method ARE valid in packet-up.
	if _, err := normalizeMeta(metaOptions{UplinkDataPlacement: "header"}, modePacketUp); err != nil {
		t.Fatalf("uplink header in packet-up should be valid: %v", err)
	}
	if _, err := normalizeMeta(metaOptions{UplinkHTTPMethod: "get"}, modePacketUp); err != nil {
		t.Fatalf("GET method in packet-up should be valid: %v", err)
	}
}

// --- uplink method on the wire -------------------------------------------------

func TestUplinkMethodUpperCased(t *testing.T) {
	m, err := normalizeMeta(metaOptions{UplinkHTTPMethod: "put"}, modePacketUp)
	if err != nil {
		t.Fatalf("normalizeMeta: %v", err)
	}
	if m.uplinkHTTPMethod != "PUT" {
		t.Fatalf("uplinkHTTPMethod = %q, want PUT", m.uplinkHTTPMethod)
	}
}

// --- chunk size defaults -------------------------------------------------------

func TestUplinkChunkSizeDefaults(t *testing.T) {
	cookie, _ := normalizeMeta(metaOptions{UplinkDataPlacement: "cookie"}, modePacketUp)
	if cookie.uplinkChunkSize != (intRange{2048, 3072}) {
		t.Fatalf("cookie chunk default = %v, want {2048 3072}", cookie.uplinkChunkSize)
	}
	header, _ := normalizeMeta(metaOptions{UplinkDataPlacement: "header"}, modePacketUp)
	if header.uplinkChunkSize != (intRange{3000, 4000}) {
		t.Fatalf("header chunk default = %v, want {3000 4000}", header.uplinkChunkSize)
	}
	// floor of 64.
	floored, _ := normalizeMeta(metaOptions{UplinkDataPlacement: "header", UplinkChunkSize: "10-20"}, modePacketUp)
	if floored.uplinkChunkSize.min != 64 {
		t.Fatalf("chunk floor not applied: %v", floored.uplinkChunkSize)
	}
}

// itoa is a tiny local helper to avoid importing strconv in the test for one use.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
