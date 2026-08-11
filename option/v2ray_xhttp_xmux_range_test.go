package option

import (
	"testing"

	"github.com/sagernet/sing/common/json"
)

// SPECS/TASKS/059-XHTTP_XMUX §3
//
// xmux sections travel inside subscription configs authored against Xray and
// sing-box-extended, which spell ranges as arrays. Our own XHTTP options spell
// them as "min-max" strings. A config that uses either spelling must load, and
// must mean the same thing.
func TestXmuxRangeUnmarshal(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected XmuxRange
	}{
		{"our string form", `"600-900"`, "600-900"},
		{"single value string", `"900"`, "900"},
		{"bare number", `900`, "900"},
		{"xray array form", `[600,900]`, "600-900"},
		{"single element array", `[900]`, "900"},
		{"empty string", `""`, ""},
		{"whitespace is trimmed", `" 600-900 "`, "600-900"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var value XmuxRange
			if err := json.Unmarshal([]byte(testCase.input), &value); err != nil {
				t.Fatalf("unmarshal %s: %v", testCase.input, err)
			}
			if value != testCase.expected {
				t.Fatalf("unmarshal %s = %q, want %q", testCase.input, value, testCase.expected)
			}
		})
	}
}

func TestXmuxRangeUnmarshalRejects(t *testing.T) {
	testCases := []struct {
		name  string
		input string
	}{
		{"object", `{"from":1}`},
		{"three elements", `[1,2,3]`},
		{"boolean", `true`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var value XmuxRange
			if err := json.Unmarshal([]byte(testCase.input), &value); err == nil {
				t.Fatalf("unmarshal %s succeeded, want an error", testCase.input)
			}
		})
	}
}

// TestXmuxOptionsRoundTrip: a whole xmux section in the Xray spelling must load,
// and an unset section must stay absent when the config is written back out.
func TestXmuxOptionsRoundTrip(t *testing.T) {
	const input = `{"max_concurrency":[2,8],"h_max_request_times":"600-900","h_keep_alive_period":30}`
	var options V2RayXHTTPXmuxOptions
	if err := json.Unmarshal([]byte(input), &options); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if options.MaxConcurrency != "2-8" {
		t.Fatalf("max_concurrency = %q, want \"2-8\"", options.MaxConcurrency)
	}
	if options.HMaxRequestTimes != "600-900" {
		t.Fatalf("h_max_request_times = %q, want \"600-900\"", options.HMaxRequestTimes)
	}
	if options.HKeepAlivePeriod != 30 {
		t.Fatalf("h_keep_alive_period = %d, want 30", options.HKeepAlivePeriod)
	}

	encoded, err := json.Marshal(&options)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded V2RayXHTTPXmuxOptions
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded != options {
		t.Fatalf("round trip changed the options: %+v != %+v", reloaded, options)
	}
	if reloaded.MaxConnections != "" {
		t.Fatalf("max_connections = %q, want it to stay unset", reloaded.MaxConnections)
	}
}
