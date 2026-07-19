//go:build !darwin && !linux

package dialer

import (
	"syscall"
	"testing"
)

func udpSocketDFSet(t *testing.T, _ syscall.Conn) bool {
	t.Helper()
	t.Skip("DF socket-flag introspection implemented for darwin and linux only")
	return false
}
