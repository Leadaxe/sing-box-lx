//go:build darwin

package dialer

import (
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// udpSocketDFSet reports whether the socket has "don't fragment" forced on
// (control.DisableUDPFragment sets IP_DONTFRAG=1 on darwin).
func udpSocketDFSet(t *testing.T, sysConn syscall.Conn) bool {
	t.Helper()
	rawConn, err := sysConn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var (
		value   int
		sockErr error
		ctrlErr error
	)
	ctrlErr = rawConn.Control(func(fd uintptr) {
		value, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_DONTFRAG)
	})
	if ctrlErr != nil {
		t.Fatal(ctrlErr)
	}
	if sockErr != nil {
		t.Fatal(sockErr)
	}
	return value != 0
}
