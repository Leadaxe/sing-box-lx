//go:build darwin

// The stalled peer relies on SO_NWRITE counting unsent bytes: on Linux a zero
// window leaves TCP_INFO.Unacked at 0, so the stand needs a data-dropping
// filter there (SPEC 163 TEST_PLAN).

package tf_test

import (
	"context"
	"crypto/tls"
	"net"
	"syscall"
	"testing"
	"time"

	tf "github.com/sagernet/sing-box/common/tlsfragment"

	"github.com/stretchr/testify/require"
)

// stalledPeer returns a client TCP connection whose peer accepts but never
// reads, with the peer's receive window already full: every byte written next
// stays unacknowledged, as on a path that drops data after the handshake.
func stalledPeer(t *testing.T) *net.TCPConn {
	t.Helper()
	listenConfig := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		})
	}}
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	tcpConn := conn.(*net.TCPConn)
	require.NoError(t, tcpConn.SetWriteBuffer(1<<20))
	peer := <-accepted
	t.Cleanup(func() {
		if peer != nil {
			peer.Close()
		}
	})
	filler := make([]byte, 64*1024)
	_ = tcpConn.SetWriteDeadline(time.Now().Add(time.Second))
	for i := 0; i < 16; i++ {
		if _, err = tcpConn.Write(filler); err != nil {
			break
		}
	}
	_ = tcpConn.SetWriteDeadline(time.Time{})
	return tcpConn
}

func TestTLSFragmentAckWaitHonoursHandshakeContext(t *testing.T) {
	t.Parallel()
	tcpConn := stalledPeer(t)
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), true, false, 0), &tls.Config{
		ServerName: "www.example.com",
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- tlsConn.HandshakeContext(ctx) }()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Less(t, time.Since(start), 3*time.Second)
	case <-time.After(5 * time.Second):
		tcpConn.Close()
		t.Fatal("handshake did not return after its context expired")
	}
}
