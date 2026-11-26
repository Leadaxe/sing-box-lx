package http

// lx:begin SPEC 138

import (
	"io"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

// A CONNECT status that says the destination failed is the destination's; every
// other failure of the open is the node's.
func TestLxDestinationRefused(t *testing.T) {
	for _, c := range []struct {
		err         error
		destination bool
	}{
		{E.New("unexpected status: ", "502 Bad Gateway"), true},
		{E.New("unexpected status: ", "504 Gateway Timeout"), true},
		{E.New("unexpected status: ", "403 Forbidden"), true},
		{E.New("unexpected status: ", "500 Internal Server Error"), false},
		{E.New("authentication required"), false},
		{io.EOF, false},
	} {
		if adapter.IsDestinationRefusedLx(lxDestinationRefused("h", c.err)) != c.destination {
			t.Fatalf("%v: want destination %v", c.err, c.destination)
		}
	}
	if lxDestinationRefused("h", nil) != nil {
		t.Fatal("no error stays none")
	}
}

// lx:end SPEC 138
