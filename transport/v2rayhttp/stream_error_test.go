package v2rayhttp

import (
	"errors"
	"io"
	"testing"

	"golang.org/x/net/http2"
)

type resettingReader struct{}

func (resettingReader) Read([]byte) (int, error) {
	return 0, http2.StreamError{StreamID: 21, Code: http2.ErrCodeInternal, Cause: errors.New("received from peer")}
}

// lx: SPEC 082 — issue #14: the HTTP/2 library's StreamError type must not
// leave the conn, neither from a body read nor from a failed late setup.
// Since sing v0.9.6 the conn itself is pure upstream: baderror.WrapH2 boxes the
// error into its own protocolError type (SPEC 095), which is what keeps x/net's
// readLoop — a direct `err.(StreamError)` assertion — from spinning. The
// wrapper still exposes Unwrap by design, so this guard checks the concrete
// type, not errors.As: a regression to a raw StreamError is what it must catch.
func TestHTTP2ConnHidesStreamError(t *testing.T) {
	conn := NewLateHTTPConn(io.Discard, func() {})
	conn.Setup(resettingReader{}, nil)
	_, err := conn.Read(make([]byte, 16))
	if _, raw := err.(http2.StreamError); err == nil || raw {
		t.Fatalf("body read leaked StreamError: %v", err)
	}

	late := NewLateHTTPConn(io.Discard, func() {})
	late.Setup(nil, http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal})
	_, err = late.Read(make([]byte, 16))
	if _, raw := err.(http2.StreamError); err == nil || raw {
		t.Fatalf("late setup leaked StreamError: %v", err)
	}
}
