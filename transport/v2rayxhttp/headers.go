package v2rayxhttp

import (
	"net/http"
	"strconv"
	"time"
)

// lx: SPEC 142 — default request headers, as Xray's
// TryDefaultHeadersWith(header, "fetch") (common/utils/browser.go): without a
// User-Agent in `headers` every request carried net/http's
// "Go-http-client/2.0", a one-token signature for anything inspecting the
// request. Xray presents a Chrome fetch() — the UA, the client hints and the
// Sec-Fetch-* / Priority / Cache-Control set a page's fetch() sends.
//
// A configured User-Agent is kept verbatim, with two of Xray's special values:
// "chrome" asks for this set, "golang" exposes net/http's default. Xray's
// firefox/safari/edge/curl values are not mapped — they are sent as given.

// chromeStableStart anchors the Chrome major version: Chrome 144 was released
// 2026-01-13, and a stable major ships roughly every five weeks. The version is
// computed per process from the date, so a build does not freeze on the Chrome
// of its release day.
var chromeStableStart = time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC)

const (
	chromeStableStartMajor = 144
	chromeReleaseCadence   = 35 * 24 * time.Hour
)

// chromeMajor is the Chrome major version to present: the one that was stable
// a cadence ago, so a freshly released major is not claimed on its first day.
func chromeMajor(now time.Time) int {
	elapsed := now.Sub(chromeStableStart) - chromeReleaseCadence
	if elapsed < 0 {
		return chromeStableStartMajor
	}
	return chromeStableStartMajor + int(elapsed/chromeReleaseCadence)
}

// chromeHeaders is the Chrome fetch() header set for major.
func chromeHeaders(major int) http.Header {
	version := strconv.Itoa(major)
	return http.Header{
		// Direct keys keep Chrome's casing on HTTP/1.1 (Header.Set would
		// canonicalize them); HTTP/2 lowercases every name anyway.
		"Sec-CH-UA":          {`"Chromium";v="` + version + `", "Google Chrome";v="` + version + `", "Not-A.Brand";v="99"`},
		"Sec-CH-UA-Mobile":   {"?0"},
		"Sec-CH-UA-Platform": {`"Windows"`},
		"DNT":                {"1"},
		"User-Agent":         {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + ".0.0.0 Safari/537.36"},
		"Accept-Language":    {"en-US,en;q=0.9"},
		"Sec-Fetch-Mode":     {"cors"},
		"Sec-Fetch-Dest":     {"empty"},
		"Sec-Fetch-Site":     {"same-origin"},
		"Priority":           {"u=1, i"},
		"Cache-Control":      {"no-cache"},
		"Pragma":             {"no-cache"},
		"Accept":             {"*/*"},
	}
}

// applyDefaultHeaders fills headers with the Chrome fetch() set when no
// User-Agent is configured or it is "chrome"; "golang" removes the User-Agent
// so net/http's default goes out; any other value is kept as configured. A
// header the user did set is never overwritten.
func applyDefaultHeaders(headers http.Header) {
	switch headers.Get("User-Agent") {
	case "":
	case "chrome":
		headers.Del("User-Agent")
	case "golang":
		headers.Del("User-Agent")
		return
	default:
		return
	}
	for key, values := range chromeHeaders(chromeMajor(timeNow())) {
		if _, set := headers[key]; set || headers.Get(key) != "" {
			continue
		}
		headers[key] = values
	}
}
