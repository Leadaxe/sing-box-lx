//go:build with_quic

package trusttunnel

import (
	"io"
	"net"
	"testing"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	E "github.com/sagernet/sing/common/exceptions"
)

func TestHTTP3(t *testing.T) {
	runEndToEnd(t, ProtocolHTTP3)
}

func TestStreamClosedErrorHTTP3(t *testing.T) {
	for _, testCase := range []struct {
		err      error
		expected error
	}{
		{&http3.Error{Remote: true, ErrorCode: http3.ErrCodeNoError}, io.EOF},
		{E.Cause(&http3.Error{ErrorCode: http3.ErrCodeRequestCanceled}, "read"), net.ErrClosed},
		{&quic.StreamError{ErrorCode: quic.StreamErrorCode(http3.ErrCodeRequestCanceled)}, net.ErrClosed},
		{&http3.Error{Remote: true, ErrorCode: http3.ErrCodeFrameError}, nil},
		{&http3.Error{Remote: true, ErrorCode: errorCodeAuthenticationRequired}, nil},
	} {
		if got := streamClosedError(testCase.err); got != testCase.expected {
			t.Errorf("%v: got %v, want %v", testCase.err, got, testCase.expected)
		}
	}
}
