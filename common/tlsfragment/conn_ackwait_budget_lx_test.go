//go:build darwin

// The stalled peer relies on SO_NWRITE counting unsent bytes: on Linux a zero
// window leaves TCP_INFO.Unacked at 0, so the stand needs a data-dropping
// filter there (SPEC 163 TEST_PLAN).

package tf

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A first Write whose fragments are never acknowledged returns once the budget
// is spent, with every byte handed to the socket. lx: SPEC 163.
func TestAckWaitBudgetLx(t *testing.T) {
	saved := ackWaitBudgetLx
	ackWaitBudgetLx = 500 * time.Millisecond
	t.Cleanup(func() { ackWaitBudgetLx = saved })

	listenConfig := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		})
	}}
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	tcpConn := conn.(*net.TCPConn)
	require.NoError(t, tcpConn.SetWriteBuffer(1<<20))
	peer := <-accepted
	defer peer.Close()
	filler := make([]byte, 64*1024)
	_ = tcpConn.SetWriteDeadline(time.Now().Add(time.Second))
	for i := 0; i < 16; i++ {
		if _, err = tcpConn.Write(filler); err != nil {
			break
		}
	}
	_ = tcpConn.SetWriteDeadline(time.Time{})

	fragmentConn := NewConn(tcpConn, context.Background(), true, false, 0)
	start := time.Now()
	for i := 0; i < 3; i++ {
		require.NoError(t, fragmentConn.writeAndWaitAckLx([]byte("fragment")))
	}
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, ackWaitBudgetLx)
	require.Less(t, elapsed, 2*ackWaitBudgetLx)
}
