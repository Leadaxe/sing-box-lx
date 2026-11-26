// lx: SPECS/TASKS/091 §2 — `profile: standard` without `uri` used to fail with
// two different texts depending on `vhttp`: `resolveVHTTP` ran first, so a
// config with `vhttp: h2` was told about h2 and only hit the real cause (the
// missing `uri`) after fixing something that was not the problem. The `uri`
// check now runs first, and its text says what to put there.
package masque

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func buildMASQUEOutbound(t *testing.T, options option.MASQUEOutboundOptions) error {
	t.Helper()
	_, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "masque-test", options)
	return err
}

func standardOptions(vhttp, uri string) option.MASQUEOutboundOptions {
	return option.MASQUEOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
		Profile:       "standard",
		VHTTP:         vhttp,
		URI:           uri,
		IP:            "172.16.0.2/32",
	}
}

// One cause, one text — whatever `vhttp` says.
func TestLxMASQUEStandardWithoutURIIsOneError(t *testing.T) {
	t.Parallel()
	var first string
	for _, vhttp := range []string{"h2", "h3", "auto", ""} {
		err := buildMASQUEOutbound(t, standardOptions(vhttp, ""))
		if err == nil {
			t.Fatalf("vhttp %q: standard without uri must be rejected", vhttp)
		}
		if !strings.Contains(err.Error(), "uri is required for the standard profile") {
			t.Fatalf("vhttp %q: expected the uri error, got %v", vhttp, err)
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("vhttp %q: the text must not depend on vhttp:\n  %q\n  %q", vhttp, first, err.Error())
		}
	}
	// the text has to be self-sufficient: it must show the shape expected
	if !strings.Contains(first, "CONNECT-IP") || !strings.Contains(first, "https://") {
		t.Fatalf("the uri error must show what to write, got %q", first)
	}
}

// The h2 error is still honest — it just no longer masks a missing `uri`.
func TestLxMASQUEStandardH2StillRejectedWhenURIIsSet(t *testing.T) {
	t.Parallel()
	err := buildMASQUEOutbound(t, standardOptions("h2", "https://example.com/.well-known/masque/ip/*/*/"))
	if err == nil {
		t.Fatal("vhttp h2 on the standard profile must be rejected")
	}
	if !strings.Contains(err.Error(), "vhttp h2 is not implemented for the standard profile") {
		t.Fatalf("expected the h2 error, got %v", err)
	}
}

// The cloudflare profile carries its own default uri and must not be touched.
func TestLxMASQUECloudflareNeedsNoURI(t *testing.T) {
	t.Parallel()
	options := option.MASQUEOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
		Profile:       "cloudflare",
		IP:            "172.16.0.2/32",
	}
	err := buildMASQUEOutbound(t, options)
	if err != nil && strings.Contains(err.Error(), "uri is required") {
		t.Fatalf("cloudflare must take its uri from the profile, got %v", err)
	}
}
