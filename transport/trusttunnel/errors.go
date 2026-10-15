package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/sagernet/sing/common/baderror"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/net/http2"
)

// ErrAuthenticationRequired is reported when the server refuses the credentials
// by closing the session with the protocol's "authentication required" error
// code. A 407 answer is a *StatusError that also matches it with errors.Is.
var ErrAuthenticationRequired = E.New("trusttunnel: authentication required")

// errorCodeAuthenticationRequired is the HTTP/2 GOAWAY and HTTP/3 connection
// error code the endpoint uses for an authentication failure.
const errorCodeAuthenticationRequired = 0x1f

// Warning codes of the X-Warning header.
const (
	WarningConnectionFailed = 300
	WarningHostUnreachable  = 301
	WarningTimeout          = 302
	WarningDNSNonRoutable   = 310
	WarningDNSLoopback      = 311
)

const (
	warningTextConnectionFailed = "Connection failed for some reason"
	warningTextHostUnreachable  = "Remote host is unreachable"
	warningTextTimeout          = "Connection timed out"
)

// StatusError is a CONNECT answered with anything but 200.
type StatusError struct {
	StatusCode int
	// Warning is the numeric code of X-Warning, 0 when absent or unparsable.
	Warning int
	// WarningText is the text of X-Warning after the code, or the whole value
	// when it has no code.
	WarningText string
	// Host is X-Adguard-Vpn-Error: the hostname of a DNS failure (310/311).
	Host string
}

func (e *StatusError) Error() string {
	var builder strings.Builder
	builder.WriteString("trusttunnel: server responded ")
	builder.WriteString(strconv.Itoa(e.StatusCode))
	if text := http.StatusText(e.StatusCode); text != "" {
		builder.WriteString(" ")
		builder.WriteString(text)
	}
	if e.Warning != 0 || e.WarningText != "" {
		builder.WriteString(": ")
		if e.Warning != 0 {
			builder.WriteString(strconv.Itoa(e.Warning))
			if e.WarningText != "" {
				builder.WriteString(" - ")
			}
		}
		builder.WriteString(e.WarningText)
	}
	if e.Host != "" {
		builder.WriteString(" (")
		builder.WriteString(e.Host)
		builder.WriteString(")")
	}
	return builder.String()
}

func (e *StatusError) Is(target error) bool {
	return target == ErrAuthenticationRequired && e.StatusCode == http.StatusProxyAuthRequired
}

// refusesSession reports whether the answer is one of the endpoint's
// authentication failure codes (407, or 403/404/405 when configured so): the
// protocol requires the client to drop its session on it.
func (e *StatusError) refusesSession() bool {
	switch e.StatusCode {
	case http.StatusProxyAuthRequired, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	default:
		return false
	}
}

// NewStatusError builds a *StatusError from the status code and headers of an
// answer to CONNECT.
func NewStatusError(statusCode int, header http.Header) *StatusError {
	statusError := &StatusError{
		StatusCode: statusCode,
		Host:       header.Get(headerDNSError),
	}
	if warning := strings.TrimSpace(header.Get(headerWarning)); warning != "" {
		statusError.Warning, statusError.WarningText = parseWarning(warning)
	}
	return statusError
}

func parseWarning(value string) (int, string) {
	codeString, text, found := strings.Cut(value, " - ")
	if !found {
		codeString, text, _ = strings.Cut(value, " ")
	}
	code, err := strconv.Atoi(strings.TrimSpace(codeString))
	if err != nil {
		return 0, value
	}
	return code, strings.TrimSpace(text)
}

// IsAuthenticationRequired reports whether err is a refusal of the credentials:
// a 407 answer or the session closed with the "authentication required" code.
func IsAuthenticationRequired(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAuthenticationRequired) {
		return true
	}
	var goAway http2.GoAwayError
	if errors.As(err, &goAway) && goAway.ErrCode == errorCodeAuthenticationRequired {
		return true
	}
	return quicAuthenticationError != nil && quicAuthenticationError(err)
}

// quicAuthenticationError recognises the HTTP/3 form of the authentication
// failure; set by the with_quic half of the package.
var quicAuthenticationError func(error) bool

// streamClosedError reports a stream that either side ended normally rather
// than with a protocol error: io.EOF for NO_ERROR (HTTP/2) and H3_NO_ERROR,
// net.ErrClosed for CANCEL (HTTP/2), H3_REQUEST_CANCELLED and a closed HTTP/2
// connection, nil otherwise.
func streamClosedError(err error) error {
	if err == nil {
		return nil
	}
	// x/net http2 fails the request bodies of a connection the client closed
	// with an unexported error.
	if baderror.Contains(err, "client disconnected") {
		return net.ErrClosed
	}
	var streamError http2.StreamError
	if errors.As(err, &streamError) {
		switch streamError.Code {
		case http2.ErrCodeNo:
			return io.EOF
		case http2.ErrCodeCancel:
			return net.ErrClosed
		default:
			return nil
		}
	}
	return quicStreamClosedError(err)
}

// isStreamClosed reports whether err is a normal end of a stream, see
// streamClosedError.
func isStreamClosed(err error) bool {
	return streamClosedError(err) != nil
}

// warningFor maps a dial error onto the X-Warning value the official endpoint
// would send for it.
func warningFor(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return strconv.Itoa(WarningTimeout) + " - " + warningTextTimeout
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return strconv.Itoa(WarningHostUnreachable) + " - " + warningTextHostUnreachable
	default:
		return strconv.Itoa(WarningConnectionFailed) + " - " + warningTextConnectionFailed
	}
}
