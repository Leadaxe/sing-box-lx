package v2rayxhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// SPECS/TASKS/142-XHTTP_CLIENT_AUDIT — one test per finding of the 2026-10-29
// inspection of this client against Xray splithttp (1b1647cd).

// §1 — a stream-up upload POST the server refuses (400: an Xray server in
// packet-up mode answers it to a POST without seq) must fail Write promptly;
// before the fix the response was drained without a look at its status and
// Write parked on the pipe until the deadline.
func TestStreamUpRefusedUploadFailsWrite(t *testing.T) {
	t.Parallel()
	addr := h2cServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	client := liveH2CClient(t, addr, modeStreamUp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	if err != nil {
		// The refusal may land before the raise: that is a failed dial with the
		// same cause, also acceptable.
		if !strings.Contains(err.Error(), "unexpected upload status") {
			t.Fatalf("DialContext: %v", err)
		}
		return
	}
	defer conn.Close()
	deadline := time.Now().Add(writeFreeBudget)
	for {
		_, err := conn.Write([]byte("payload"))
		if err != nil {
			if !strings.Contains(err.Error(), "unexpected upload status") {
				t.Fatalf("Write error = %v, want the upload status", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Write keeps succeeding after the server refused the upload stream")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// §1 — the server ending the upload response (it stays open for the session's
// life; its end is the end of the upload) breaks the pipe too.
func TestStreamUpEndedUploadFailsWrite(t *testing.T) {
	t.Parallel()
	addr := h2cServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// Read the first bytes, then end the response.
		buffer := make([]byte, 16)
		r.Body.Read(buffer)
	})
	client := liveH2CClient(t, addr, modeStreamUp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(writeFreeBudget)
	for {
		_, err := conn.Write([]byte("payload"))
		if err != nil {
			if !errors.Is(err, errUploadStreamEnded) {
				t.Fatalf("Write error = %v, want %v", err, errUploadStreamEnded)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Write keeps succeeding after the server ended the upload response")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// §2 — Xray's GetNormalizedPath: the base path ends in "/" whenever session OR
// seq is placed in the path. With the session off the path and the seq on it
// (the default seq placement) the download GET used to go to "/up" against a
// server prefix-matching "/up/".
func TestBasePathSlashFollowsSeqPlacement(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		mode          string
		opts          metaOptions
		sessionID     string
		seqStr        string
		path          string
		wantPath      string
		wantQuerySess bool
	}{
		{"packet-up download, session in header, seq in path", modePacketUp, metaOptions{SessionPlacement: "header"}, "sid", "", "/up", "/up/", false},
		{"packet-up upload, session in header, seq in path", modePacketUp, metaOptions{SessionPlacement: "header"}, "sid", "7", "/up", "/up/7", false},
		{"packet-up download, session in query, seq in path", modePacketUp, metaOptions{SessionPlacement: "query"}, "sid", "", "/up", "/up/", true},
		{"stream-up download, session in cookie, seq in path", modeStreamUp, metaOptions{SessionPlacement: "cookie"}, "sid", "", "/up", "/up/", false},
		{"configured slash kept", modePacketUp, metaOptions{SessionPlacement: "header"}, "sid", "", "/up/", "/up/", false},
		{"both off the path: verbatim (SPEC 002 §9)", modePacketUp, metaOptions{SessionPlacement: "header", SeqPlacement: "query"}, "sid", "7", "/up", "/up", false},
		{"session in path: slash from the segment", modePacketUp, metaOptions{}, "sid", "7", "/up", "/up/sid/7", false},
		{"root", modePacketUp, metaOptions{SessionPlacement: "header"}, "sid", "", "/", "/", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			meta, err := normalizeMeta(testCase.opts, testCase.mode)
			if err != nil {
				t.Fatalf("normalizeMeta: %v", err)
			}
			client := &Client{
				scheme:       "https",
				host:         "example.com",
				serverAddr:   M.ParseSocksaddr("example.com:443"),
				path:         testCase.path,
				mode:         testCase.mode,
				paddingRange: intRange{0, 0},
				meta:         meta,
			}
			request := mustRequest(t, client, http.MethodGet, testCase.sessionID, testCase.seqStr)
			if request.URL.Path != testCase.wantPath {
				t.Fatalf("path = %q, want %q", request.URL.Path, testCase.wantPath)
			}
			if testCase.wantQuerySess && request.URL.Query().Get("x_session") != "sid" {
				t.Fatalf("session missing from the query: %q", request.URL.RawQuery)
			}
		})
	}
}

// §3/§4 — the HTTP/2 transport as Xray builds it: IdleConnTimeout 300 s and
// h_keep_alive_period 0 → 45 s of h2 pings, negative → none.
func TestHTTP2TransportParity(t *testing.T) {
	for _, testCase := range []struct {
		in, want time.Duration
	}{
		{0, 45 * time.Second},
		{25 * time.Second, 25 * time.Second},
		{-time.Second, 0},
	} {
		transport := newHTTP2Transport(nil, testCase.in)
		if transport.ReadIdleTimeout != testCase.want {
			t.Fatalf("ReadIdleTimeout(%v) = %v, want %v", testCase.in, transport.ReadIdleTimeout, testCase.want)
		}
		if transport.IdleConnTimeout != 300*time.Second {
			t.Fatalf("IdleConnTimeout = %v, want 300s", transport.IdleConnTimeout)
		}
	}
}

// §5 — without a configured User-Agent the client presents Chrome's fetch()
// headers, as Xray; a configured one is kept, "chrome" asks for the set,
// "golang" exposes net/http's default; a user-set header is never overwritten.
func TestDefaultHeadersChromeFetch(t *testing.T) {
	headers := http.Header{}
	applyDefaultHeaders(headers)
	userAgent := headers.Get("User-Agent")
	if !strings.HasPrefix(userAgent, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/") || !strings.HasSuffix(userAgent, ".0.0.0 Safari/537.36") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	for key, want := range map[string]string{
		"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty", "Sec-Fetch-Site": "same-origin",
		"Priority": "u=1, i", "Cache-Control": "no-cache", "Pragma": "no-cache", "Accept": "*/*",
		"Accept-Language": "en-US,en;q=0.9",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"Sec-CH-UA", "Sec-CH-UA-Mobile", "Sec-CH-UA-Platform", "DNT"} {
		if _, ok := headers[key]; !ok {
			t.Fatalf("%s missing (keys: %v)", key, headers)
		}
	}
	if major := chromeMajor(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)); major != 151 {
		t.Fatalf("chromeMajor(2026-10-29) = %d, want 151", major)
	}
	if major := chromeMajor(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); major != 144 {
		t.Fatalf("chromeMajor before the anchor = %d, want 144", major)
	}

	kept := http.Header{"User-Agent": {"curl/8.0"}, "Accept": {"text/html"}}
	applyDefaultHeaders(kept)
	if kept.Get("User-Agent") != "curl/8.0" || kept.Get("Accept") != "text/html" || kept.Get("Sec-Fetch-Mode") != "" {
		t.Fatalf("configured User-Agent not kept verbatim: %v", kept)
	}

	chrome := http.Header{"User-Agent": {"chrome"}, "Accept": {"text/html"}}
	applyDefaultHeaders(chrome)
	if !strings.HasPrefix(chrome.Get("User-Agent"), "Mozilla/5.0") || chrome.Get("Accept") != "text/html" {
		t.Fatalf(`"chrome" not mapped, or a user header overwritten: %v`, chrome)
	}

	golang := http.Header{"User-Agent": {"golang"}}
	applyDefaultHeaders(golang)
	if len(golang) != 0 {
		t.Fatalf(`"golang" must leave net/http's default: %v`, golang)
	}

	// On the wire: a NewClient request carries the set; a packet-up upload has
	// no Content-Type (Xray sets none).
	transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), option.V2RayXHTTPOptions{Mode: modePacketUp, Path: "/xhttp/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := transport.(*Client)
	defer client.Close()
	request := mustRequest(t, client, http.MethodPost, "sid", "0")
	client.applyUplinkData(request, []byte("payload"))
	if got := request.Header.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Fatalf("request User-Agent = %q", got)
	}
	if got := request.Header.Get("Content-Type"); got != "" {
		t.Fatalf("packet-up upload Content-Type = %q, want none", got)
	}
}

// §6 — the configured path is literal: "%" is not an escape.
func TestPathIsLiteral(t *testing.T) {
	for _, testCase := range []struct{ path, want string }{
		{"/a%2Fb/", "/a%2Fb/"},
		{"/100%/", "/100%/"},
		{"/plain/", "/plain/"},
	} {
		transport, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:443"), option.V2RayXHTTPOptions{Mode: modePacketUp, Path: testCase.path, SessionPlacement: "header"}, nil)
		if err != nil {
			t.Fatalf("NewClient(%q): %v", testCase.path, err)
		}
		client := transport.(*Client)
		request, err := client.newRequest(context.Background(), http.MethodGet, "sid", "", nil)
		client.Close()
		if err != nil {
			t.Fatalf("newRequest(%q): %v", testCase.path, err)
		}
		if request.URL.Path != testCase.want {
			t.Fatalf("path %q reached the request as %q, want %q", testCase.path, request.URL.Path, testCase.want)
		}
	}
}
