package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/net/http2"
)

func TestStatusErrorParsing(t *testing.T) {
	header := http.Header{}
	header.Set("X-Warning", "310 - DNS: resolved address in non-routable network")
	header.Set("X-Adguard-Vpn-Error", "internal.example")
	statusError := NewStatusError(http.StatusBadGateway, header)
	if statusError.Warning != WarningDNSNonRoutable || statusError.WarningText != "DNS: resolved address in non-routable network" || statusError.Host != "internal.example" {
		t.Fatalf("parsed %+v", statusError)
	}
	message := statusError.Error()
	for _, part := range []string{"502", "310", "non-routable", "internal.example"} {
		if !strings.Contains(message, part) {
			t.Errorf("%q does not contain %q", message, part)
		}
	}
	if statusError.refusesSession() || errors.Is(statusError, ErrAuthenticationRequired) {
		t.Fatal("502 treated as an authentication refusal")
	}

	header = http.Header{}
	header.Set("X-Warning", "300 - Connection failed for some reason")
	statusError = NewStatusError(http.StatusBadGateway, header)
	if statusError.Warning != WarningConnectionFailed || statusError.Host != "" {
		t.Fatalf("parsed %+v", statusError)
	}

	statusError = NewStatusError(http.StatusBadGateway, http.Header{"X-Warning": []string{"no code here"}})
	if statusError.Warning != 0 || statusError.WarningText != "no code here" {
		t.Fatalf("parsed %+v", statusError)
	}

	statusError = NewStatusError(http.StatusProxyAuthRequired, http.Header{})
	if !statusError.refusesSession() || !errors.Is(statusError, ErrAuthenticationRequired) || !IsAuthenticationRequired(E.Cause(statusError, "dial")) {
		t.Fatal("407 not recognised as an authentication refusal")
	}
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed} {
		if !NewStatusError(code, http.Header{}).refusesSession() {
			t.Errorf("%d does not refuse the session", code)
		}
	}
}

func TestAuthenticationGoAway(t *testing.T) {
	if !IsAuthenticationRequired(E.Cause(http2.GoAwayError{ErrCode: 0x1f}, "round trip")) {
		t.Fatal("GOAWAY 0x1f not recognised")
	}
	if IsAuthenticationRequired(http2.GoAwayError{ErrCode: http2.ErrCodeNo}) {
		t.Fatal("GOAWAY NO_ERROR recognised as an authentication refusal")
	}
	if IsAuthenticationRequired(nil) {
		t.Fatal("nil")
	}
}

func TestWarningFor(t *testing.T) {
	for _, testCase := range []struct {
		err      error
		expected string
	}{
		{nil, "300 - Connection failed for some reason"},
		{errors.New("refused"), "300 - Connection failed for some reason"},
		{context.DeadlineExceeded, "302 - Connection timed out"},
		{E.Cause(os.ErrDeadlineExceeded, "dial"), "302 - Connection timed out"},
		{E.Cause(syscall.EHOSTUNREACH, "dial"), "301 - Remote host is unreachable"},
	} {
		if got := warningFor(testCase.err); got != testCase.expected {
			t.Errorf("%v: got %q, want %q", testCase.err, got, testCase.expected)
		}
	}
}

func TestStreamClosedError(t *testing.T) {
	for _, testCase := range []struct {
		err      error
		expected error
	}{
		{nil, nil},
		{io.ErrUnexpectedEOF, nil},
		{errFrameTooLarge, nil},
		{http2.StreamError{StreamID: 7, Code: http2.ErrCodeNo}, io.EOF},
		{E.Cause(http2.StreamError{StreamID: 7, Code: http2.ErrCodeCancel}, "read"), net.ErrClosed},
		{http2.StreamError{StreamID: 7, Code: http2.ErrCodeProtocol}, nil},
		{errors.New("client disconnected"), net.ErrClosed},
	} {
		if got := streamClosedError(testCase.err); got != testCase.expected {
			t.Errorf("%v: got %v, want %v", testCase.err, got, testCase.expected)
		}
	}
	if !E.IsClosed(wrapWriteError(http2.StreamError{StreamID: 7, Code: http2.ErrCodeNo})) {
		t.Fatal("NO_ERROR on write is not closed")
	}
}
