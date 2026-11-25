package socks

// lx:begin SPEC 138

import (
	"errors"
	"io"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

// A SOCKS reply with a destination failure code is the destination's; every other
// failure of the open is the node's.
func TestLxDestinationRefused(t *testing.T) {
	for _, c := range []struct {
		err         error
		destination bool
	}{
		{E.New("socks5: request rejected, code=", byte(1)), false}, // general failure: the node's (R5)
		{E.New("socks5: request rejected, code=", byte(2)), true},
		{E.New("socks5: request rejected, code=", byte(5)), true},
		{E.New("socks5: request rejected, code=", byte(7)), false},
		{E.New("socks4: request rejected, code= ", byte(91)), true},
		{E.New("socks4: request rejected, code= ", byte(92)), false},
		{io.EOF, false},
		{E.Cause(io.ErrUnexpectedEOF, "read socks5 response"), false},
		{errors.New("connection refused"), false},
	} {
		_, err := lxDestinationRefusedTag("s", nil, c.err)
		if adapter.IsDestinationRefusedLx(err) != c.destination {
			t.Fatalf("%v: destination %v, want %v", c.err, !c.destination, c.destination)
		}
		if !errors.Is(err, c.err) {
			t.Fatalf("%v: the error is kept", c.err)
		}
		var refused *adapter.DestinationRefusedLx
		if errors.As(err, &refused) && refused.Tag != "s" {
			t.Fatal("the mark carries the tag of the outbound that set it")
		}
	}
}

// lx:end SPEC 138
