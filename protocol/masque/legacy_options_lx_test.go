// lx: SPEC 062 — folding the deprecated flat options onto `vhttp` + `tls`.
//
// Decoding is covered where the registry lives; these tests exercise the
// reconciliation itself, including the asymmetry forced by bool fields that
// cannot tell "absent" from "false".
package masque

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestResolveLegacyVHTTP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		network string
		vhttp   string
		want    string
		wantErr bool
	}{
		{name: "legacy only", network: "h2", want: "h2"},
		{name: "new only", vhttp: "h2", want: "h2"},
		{name: "both agree", network: "h2", vhttp: "h2", want: "h2"},
		{name: "both disagree", network: "h3", vhttp: "h2", wantErr: true},
		{name: "neither", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := &option.MASQUEOutboundOptions{VHTTP: tc.vhttp}
			//nolint:staticcheck
			opts.Network = tc.network
			err := resolveLegacyOptions(context.Background(), opts)
			if tc.wantErr {
				if err == nil {
					t.Fatal("conflicting HTTP version must be rejected")
				}
				if !strings.Contains(err.Error(), "network") {
					t.Errorf("error should name the offending field, got %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.VHTTP != tc.want {
				t.Errorf("vhttp = %q, want %q", opts.VHTTP, tc.want)
			}
		})
	}
}

func TestResolveLegacySNI(t *testing.T) {
	t.Parallel()
	t.Run("legacy fills empty", func(t *testing.T) {
		opts := &option.MASQUEOutboundOptions{}
		//nolint:staticcheck
		opts.SNI = "legacy.example"
		if err := resolveLegacyOptions(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if opts.TLS.ServerName != "legacy.example" {
			t.Errorf("server_name = %q", opts.TLS.ServerName)
		}
	})

	t.Run("conflict rejected", func(t *testing.T) {
		opts := &option.MASQUEOutboundOptions{}
		opts.TLS = &option.OutboundTLSOptions{ServerName: "new.example"}
		//nolint:staticcheck
		opts.SNI = "legacy.example"
		err := resolveLegacyOptions(context.Background(), opts)
		if err == nil {
			t.Fatal("disagreeing sni/server_name must be rejected")
		}
		// The message has to name both, or the user cannot tell what to remove.
		if !strings.Contains(err.Error(), "legacy.example") || !strings.Contains(err.Error(), "new.example") {
			t.Errorf("error should quote both values, got %q", err)
		}
	})

	t.Run("agreeing is fine", func(t *testing.T) {
		opts := &option.MASQUEOutboundOptions{}
		opts.TLS = &option.OutboundTLSOptions{ServerName: "same.example"}
		//nolint:staticcheck
		opts.SNI = "same.example"
		if err := resolveLegacyOptions(context.Background(), opts); err != nil {
			t.Fatalf("identical values must not conflict: %v", err)
		}
	})
}

// TestResolveLegacyBools: an unset bool and an explicit false are the same
// value here, so only a legacy true can carry over — and a legacy false must
// never be mistaken for a conflict against a new false.
func TestResolveLegacyBools(t *testing.T) {
	t.Parallel()

	t.Run("legacy true wins", func(t *testing.T) {
		opts := &option.MASQUEOutboundOptions{}
		//nolint:staticcheck
		opts.SkipCertVerify = true
		//nolint:staticcheck
		opts.RecordFragment = true
		//nolint:staticcheck
		opts.Fragment = true
		if err := resolveLegacyOptions(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if !opts.TLS.Insecure || !opts.TLS.RecordFragment || !opts.TLS.Fragment {
			t.Errorf("legacy true must carry over: %+v", opts.TLS)
		}
	})

	t.Run("legacy false is not a conflict", func(t *testing.T) {
		opts := &option.MASQUEOutboundOptions{}
		opts.TLS = &option.OutboundTLSOptions{Insecure: true}
		//nolint:staticcheck
		opts.SkipCertVerify = false
		if err := resolveLegacyOptions(context.Background(), opts); err != nil {
			t.Fatalf("an unset legacy bool must not conflict: %v", err)
		}
		if !opts.TLS.Insecure {
			t.Error("explicit tls.insecure must survive")
		}
	})
}

func TestResolveLegacyFallbackDelay(t *testing.T) {
	t.Parallel()
	opts := &option.MASQUEOutboundOptions{}
	//nolint:staticcheck
	opts.FragmentFallbackDelay = badoption.Duration(1234)
	if err := resolveLegacyOptions(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if opts.TLS.FragmentFallbackDelay != badoption.Duration(1234) {
		t.Errorf("fallback delay = %v", opts.TLS.FragmentFallbackDelay)
	}
}

// TestResolveLegacyAllocatesTLS: downstream code reads options.TLS
// unconditionally, so resolution must leave a non-nil block behind even when
// the config carried none.
func TestResolveLegacyAllocatesTLS(t *testing.T) {
	t.Parallel()
	opts := &option.MASQUEOutboundOptions{}
	if err := resolveLegacyOptions(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if opts.TLS == nil {
		t.Fatal("TLS block must be allocated so later code can read it")
	}
}

// TestResolveLegacyNoReportWithoutLegacy: a config already on the new shape
// must not be told it is deprecated.
func TestResolveLegacyNoReportWithoutLegacy(t *testing.T) {
	t.Parallel()
	opts := &option.MASQUEOutboundOptions{VHTTP: "h2"}
	opts.TLS = &option.OutboundTLSOptions{ServerName: "new.example"}
	// No deprecation manager in this context, so Report is a no-op either way;
	// what matters is that resolution succeeds and changes nothing.
	if err := resolveLegacyOptions(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if opts.VHTTP != "h2" || opts.TLS.ServerName != "new.example" {
		t.Errorf("new-shape config must pass through untouched: %+v", opts)
	}
}
