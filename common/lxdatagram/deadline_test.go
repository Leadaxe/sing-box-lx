package lxdatagram

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// noDeadlineConn is a stream-carried "UDP" conn: reads block until data or
// Close, and SetReadDeadline does nothing — what vless XUDP over ws hands out.
type noDeadlineConn struct {
	net.Conn
	closed chan struct{}
}

func (c *noDeadlineConn) Read(p []byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}
func (c *noDeadlineConn) SetReadDeadline(time.Time) error  { return nil }
func (c *noDeadlineConn) SetDeadline(time.Time) error      { return nil }
func (c *noDeadlineConn) SetWriteDeadline(time.Time) error { return nil }
func (c *noDeadlineConn) NeedAdditionalReadDeadline() bool { return true }
func (c *noDeadlineConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type fixedDialer struct {
	N.Dialer
	conn net.Conn
}

func (d *fixedDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

// lx: SPEC 146 — a read on a conn without deadlines must return when quic-go
// sets one; without the wrapper it blocks until Close, which never comes.
func TestReadDeadlineDialer_wakesBlockedRead(t *testing.T) {
	raw := &noDeadlineConn{closed: make(chan struct{})}
	defer raw.Close()
	dialer := ReadDeadlineDialer(Dialer(context.Background(), "masque-client", &fixedDialer{conn: raw}, nil))
	conn, err := dialer.DialContext(context.Background(), N.NetworkUDP, M.ParseSocksaddr("192.0.2.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, readErr := conn.Read(make([]byte, 1452))
		done <- readErr
	}()
	time.Sleep(50 * time.Millisecond)
	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case readErr := <-done:
		if !os.IsTimeout(readErr) {
			t.Fatalf("read after a past deadline: got %v, want a timeout", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a past read deadline did not wake the read: quic-go would wait here forever")
	}
}

// TCP dials and conns that have deadlines of their own are handed out as they are.
func TestReadDeadlineDialer_passesThrough(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	dialer := ReadDeadlineDialer(&fixedDialer{conn: left})
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		conn, err := dialer.DialContext(context.Background(), network, M.ParseSocksaddr("192.0.2.1:443"))
		if err != nil {
			t.Fatal(err)
		}
		if conn != left {
			t.Fatalf("%s: a conn with working deadlines must not be wrapped", network)
		}
	}
}
