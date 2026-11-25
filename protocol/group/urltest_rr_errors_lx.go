package group

// lx:begin SPEC 138 round_robin: the classes of a failed open by platform
//
// ALGORITHM.md §2, "recognition by platform". An error belongs to a class by any
// link of its chain of wrappers (Unwrap, also the multi-error of a parallel dial).
//   - syscall.Errno of unix, android and darwin by its name;
//   - Windows by number: the net package of Go hands out the WSA codes as they come
//     (10051 WSAENETUNREACH, ...), and Errno.Is there matches none of the
//     syscall.E* names, which on Windows are invented values. The numbers never
//     occur on unix (errno stays below 200), so one list serves every platform;
//   - texts, compared whole with a link that wraps nothing: the gVisor stack of
//     WireGuard and AWG (transport/wireguard/device_stack_gonet.go) hands out a
//     tcpip error as errors.New(err.String()); the default dialer without an
//     interface; tun.ErrNoRoute by identity.

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"

	tun "github.com/sagernet/sing-tun"
)

// rrLocalErrnos are LOCAL (§2): no route, the network or a host on the way
// unreachable, the interface down, the address not bindable.
var rrLocalErrnos = [...]syscall.Errno{
	syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN, syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL,
	10051, 10065, 10050, 10064, 10049, // WSAENETUNREACH, WSAEHOSTUNREACH, WSAENETDOWN, WSAEHOSTDOWN, WSAEADDRNOTAVAIL
}

// rrLocalTexts are LOCAL as text: gVisor's ErrNetworkUnreachable,
// ErrHostUnreachable, ErrHostDown, ErrNoNet and ErrBadLocalAddress, and the
// default dialer that finds no interface (common/dialer).
var rrLocalTexts = [...]string{
	"network is unreachable", "no route to host", "host is down", "machine is not on the network", "bad local address",
	"no available network interface",
}

// rrRefusalErrnos are an active refusal: the far side refused or reset the open.
var rrRefusalErrnos = [...]syscall.Errno{
	syscall.ECONNREFUSED, syscall.ECONNRESET,
	10061, 10054, // WSAECONNREFUSED, WSAECONNRESET
}

// rrRefusalTexts are an active refusal as text: gVisor's ErrConnectionRefused and
// ErrConnectionReset.
var rrRefusalTexts = [...]string{"connection was refused", "connection reset by peer"}

// rrTimeoutErrno is WSAETIMEDOUT: Errno.Timeout on Windows knows only its own
// invented ETIMEDOUT.
const rrTimeoutErrno syscall.Errno = 10060

// rrTimeoutText is gVisor's ErrTimeout as text.
const rrTimeoutText = "operation timed out"

// rrLocalError reports LOCAL (§2).
func rrLocalError(err error) bool {
	if errors.Is(err, tun.ErrNoRoute) || rrErrnoIn(err, rrLocalErrnos[:]) {
		return true
	}
	return rrTextIn(err, rrLocalTexts[:])
}

// rrActiveRefusal reports a refused or reset open: an explicit REFUSE_DST of a leaf
// that goes to the destination itself (§2).
func rrActiveRefusal(err error) bool {
	return rrErrnoIn(err, rrRefusalErrnos[:]) || rrTextIn(err, rrRefusalTexts[:])
}

// rrSilence reports whether a failed open is a timeout: nobody answered.
func rrSilence(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, rrTimeoutErrno) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return rrTextIn(err, []string{rrTimeoutText})
}

// rrErrnoIn reports whether a link of err is one of errnos.
func rrErrnoIn(err error, errnos []syscall.Errno) bool {
	for _, errno := range errnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// rrTextIn reports whether a link of err that wraps nothing reads one of texts.
func rrTextIn(err error, texts []string) bool {
	switch wrapped := err.(type) {
	case nil:
		return false
	case interface{ Unwrap() error }:
		return rrTextIn(wrapped.Unwrap(), texts)
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			if rrTextIn(inner, texts) {
				return true
			}
		}
		return false
	}
	message := err.Error()
	for _, text := range texts {
		if message == text {
			return true
		}
	}
	return false
}

// lx:end SPEC 138
