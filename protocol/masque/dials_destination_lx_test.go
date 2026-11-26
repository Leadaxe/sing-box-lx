package masque

// lx:begin SPEC 054 / SPEC 138

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// MASQUE opens connections itself on its userspace stack: the groups take its
// refused open as the site's answer.
func TestDialsDestinationLx(t *testing.T) {
	var outbound adapter.Outbound = (*Outbound)(nil)
	dialer, isDialer := outbound.(adapter.DestinationDialerLx)
	if !isDialer || !dialer.DialsDestinationLx() {
		t.Fatal("MASQUE dials the destination itself")
	}
}

// lx:end SPEC 054 / SPEC 138
