package daemon

import (
	"context"
	"testing"
	"time"
)

// Regression for the launcher Interval:1000 incident (2026-08-11): the client
// sent 1000 meaning milliseconds, the server read it as time.Duration
// nanoseconds and armed a 1µs ticker, burning a full CPU core while the
// launcher window was open. Sub-floor intervals must be clamped, not obeyed.
func TestClampSubscribeInterval(t *testing.T) {
	s := NewStartedService(ServiceOptions{
		Context:     context.Background(),
		LogMaxLines: 16,
	})
	testCases := []struct {
		name     string
		raw      int64
		expected time.Duration
		wantWarn bool
	}{
		{"launcher incident value (1000ns)", 1000, minSubscribeInterval, true},
		{"one millisecond", int64(time.Millisecond), minSubscribeInterval, true},
		{"zero (field absent)", 0, minSubscribeInterval, false},
		{"negative", -5, minSubscribeInterval, false},
		{"exactly the floor", int64(minSubscribeInterval), minSubscribeInterval, false},
		{"one second", int64(time.Second), time.Second, false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			logsBefore := len(s.SavedLog())
			interval := s.clampSubscribeInterval("TestStream", testCase.raw)
			if interval != testCase.expected {
				t.Fatalf("clampSubscribeInterval(%d) = %v, expected %v", testCase.raw, interval, testCase.expected)
			}
			gotWarn := len(s.SavedLog()) > logsBefore
			if gotWarn != testCase.wantWarn {
				t.Fatalf("clampSubscribeInterval(%d): warn logged = %v, expected %v", testCase.raw, gotWarn, testCase.wantWarn)
			}
		})
	}
}
