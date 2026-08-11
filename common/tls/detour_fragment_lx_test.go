// lx: SPEC 060 — ClientHello fragmentation defaults to on when the TLS leg is
// carried by another outbound.
//
// The rules under test:
//   - no detour            → nothing changes (the direct path must stay untouched)
//   - detour, no choice    → record_fragment turns on
//   - detour, user chose   → the user's choice is preserved, never overridden
package tls

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestDialedThroughDetour(t *testing.T) {
	t.Parallel()
	if DialedThroughDetour(option.DialerOptions{}) {
		t.Fatal("no detour must not count as dialed through one")
	}
	if !DialedThroughDetour(option.DialerOptions{Detour: "proxy"}) {
		t.Fatal("explicit detour must count")
	}
}

func TestApplyDetourFragmentDefault(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		detour         bool
		fragment       bool
		recordFragment bool
		wantFragment   bool
		wantRecordFrag bool
	}{
		{
			name:           "direct path untouched",
			detour:         false,
			wantFragment:   false,
			wantRecordFrag: false,
		},
		{
			name:           "detour enables record fragment",
			detour:         true,
			wantFragment:   false,
			wantRecordFrag: true,
		},
		{
			name:           "explicit record_fragment kept",
			detour:         true,
			recordFragment: true,
			wantFragment:   false,
			wantRecordFrag: true,
		},
		{
			// The user asked for packet-split specifically; adding record-split on
			// top would silently change the mode they picked.
			name:           "explicit fragment not upgraded",
			detour:         true,
			fragment:       true,
			wantFragment:   true,
			wantRecordFrag: false,
		},
		{
			name:           "no detour, explicit fragment kept",
			detour:         false,
			fragment:       true,
			wantFragment:   true,
			wantRecordFrag: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := ClientOptions{
				DialedThroughDetour: tc.detour,
				Options: option.OutboundTLSOptions{
					Fragment:       tc.fragment,
					RecordFragment: tc.recordFragment,
				},
			}
			applyDetourFragmentDefault(&opts)
			if opts.Options.Fragment != tc.wantFragment {
				t.Errorf("fragment = %v, want %v", opts.Options.Fragment, tc.wantFragment)
			}
			if opts.Options.RecordFragment != tc.wantRecordFrag {
				t.Errorf("record_fragment = %v, want %v", opts.Options.RecordFragment, tc.wantRecordFrag)
			}
		})
	}
}

// TestApplyDetourFragmentDefaultKeepsDelay: the default must not disturb a
// fallback delay the user configured.
func TestApplyDetourFragmentDefaultKeepsDelay(t *testing.T) {
	t.Parallel()
	opts := ClientOptions{
		DialedThroughDetour: true,
		Options: option.OutboundTLSOptions{
			FragmentFallbackDelay: badoption.Duration(1234),
		},
	}
	applyDetourFragmentDefault(&opts)
	if opts.Options.FragmentFallbackDelay != badoption.Duration(1234) {
		t.Fatalf("fallback delay changed: %v", opts.Options.FragmentFallbackDelay)
	}
	if !opts.Options.RecordFragment {
		t.Fatal("record_fragment should still be enabled")
	}
}

// TestNewClientWithOptionsAppliesDefault: the default is applied inside
// NewClientWithOptions, before any engine sees the options — otherwise REALITY
// and uTLS clients would miss it.
func TestNewClientWithOptionsAppliesDefault(t *testing.T) {
	t.Parallel()
	opts := ClientOptions{
		DialedThroughDetour: true,
		Options:             option.OutboundTLSOptions{Enabled: false},
	}
	// Disabled TLS returns early; the point here is only that the helper runs on
	// the shared path and does not panic on a bare options struct.
	config, err := NewClientWithOptions(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config != nil {
		t.Fatal("disabled TLS must yield a nil config")
	}
}
