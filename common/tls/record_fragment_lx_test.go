// lx: SPEC 156 — the node's record_fragment tells "not set" from an explicit
// false; lx.sniff_guard (SPEC 060, P6) relies on it.
package tls

import (
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// TestRecordFragmentJSON: an explicit `false` must survive decoding as a set
// value, distinct from an absent key — that is the whole opt-out. lx: SPEC 156.
func TestRecordFragmentJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input   string
		wantNil bool
		want    bool
	}{
		{`{}`, true, false},
		{`{"record_fragment":false}`, false, false},
		{`{"record_fragment":true}`, false, true},
	} {
		var options option.OutboundTLSOptions
		if err := json.Unmarshal([]byte(tc.input), &options); err != nil {
			t.Fatalf("%s: %v", tc.input, err)
		}
		if (options.RecordFragment == nil) != tc.wantNil || options.RecordFragmentEnabled() != tc.want {
			t.Errorf("%s: record_fragment = %v (enabled %v)", tc.input, options.RecordFragment, options.RecordFragmentEnabled())
		}
		// Round trip keeps the explicit false: re-serialised configs (running
		// config export, lxd) must not lose the opt-out.
		encoded, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != tc.input {
			t.Errorf("round trip: got %s, want %s", encoded, tc.input)
		}
	}
}

func lxBoolPtr(b bool) *bool {
	return &b
}
