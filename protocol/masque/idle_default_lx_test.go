package masque

import (
	"testing"
	"time"

	"github.com/sagernet/sing/common/json/badoption"
)

// lx: SPEC 021 B1 — idle-suspend is OFF unless idle_timeout is positive. Absent
// (zero value), an explicit "0" and a negative value all resolve to 0, which
// means the idle watcher never starts and the tunnel stays up until Close.
func TestIdleWindowDefaultOff(t *testing.T) {
	cases := []struct {
		name string
		in   badoption.Duration
		want time.Duration
	}{
		{"absent", 0, 0},
		{"negative", badoption.Duration(-time.Second), 0},
		{"positive", badoption.Duration(5 * time.Minute), 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleWindow(tc.in); got != tc.want {
				t.Fatalf("idleWindow(%v) = %v, want %v", time.Duration(tc.in), got, tc.want)
			}
		})
	}
}
