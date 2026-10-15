//go:build with_quic

package trusttunnel

import (
	"errors"
	"io"
	"net"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
)

// quicStreamClosedError is the HTTP/3 half of streamClosedError: io.EOF for
// H3_NO_ERROR, net.ErrClosed for H3_REQUEST_CANCELLED, nil otherwise.
func quicStreamClosedError(err error) error {
	var code http3.ErrCode
	var http3Error *http3.Error
	var streamError *quic.StreamError
	switch {
	case errors.As(err, &http3Error):
		code = http3Error.ErrorCode
	case errors.As(err, &streamError):
		code = http3.ErrCode(streamError.ErrorCode)
	default:
		return nil
	}
	switch code {
	case http3.ErrCodeNoError:
		return io.EOF
	case http3.ErrCodeRequestCanceled:
		return net.ErrClosed
	default:
		return nil
	}
}
