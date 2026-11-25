package socks

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks/socks5"
)

// lx: handshake-timeout (SPEC 161) — a SOCKS server that accepts the TCP
// connection and then says nothing (on the stand: kill -STOP of the server
// process) kept the outbound's DialContext parked in the handshake for ever.
// Since the sync of 2026-11-26 upstream bounds it (8a1ddcebd: the sing client
// times the handshake out after its dial and honours the caller's context);
// these tests stay as the guard of that behaviour, on the real NewOutbound
// against loopback servers.

var testHandshakeTimeout time.Duration

func setHandshakeTimeout(t *testing.T, timeout time.Duration) {
	testHandshakeTimeout = timeout
	t.Cleanup(func() { testHandshakeTimeout = 0 })
}

// acceptOne starts a loopback listener whose first accepted conn is handed to
// serve; the returned channel is closed when serve returns.
func acceptOne(t *testing.T, serve func(conn net.Conn)) (net.Listener, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no IPv4 loopback: ", err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serve(conn)
	}()
	return listener, done
}

func newSOCKSOutbound(t *testing.T, listener net.Listener) adapter.Outbound {
	t.Helper()
	serverAddr := M.ParseSocksaddr(listener.Addr().String())
	out, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().NewLogger("socks"), "socks-out", option.SOCKSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: serverAddr.AddrString(), ServerPort: serverAddr.Port},
	})
	if err != nil {
		t.Fatal(err)
	}
	if testHandshakeTimeout > 0 {
		out.(*Outbound).client.SetHandshakeTimeout(testHandshakeTimeout)
	}
	return out
}

// silent reads whatever the client sends and never answers, until the client
// closes; it reports that close.
func silent(closed chan<- struct{}) func(conn net.Conn) {
	return func(conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}
}

// serveSOCKS5Connect answers one no-auth CONNECT with success, then runs after.
func serveSOCKS5Connect(t *testing.T, after func(conn net.Conn, reader *bufio.Reader)) func(conn net.Conn) {
	return func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		if _, err := socks5.ReadAuthRequest(reader); err != nil {
			t.Error("read auth request: ", err)
			return
		}
		if err := socks5.WriteAuthResponse(conn, socks5.AuthResponse{Method: socks5.AuthTypeNotRequired}); err != nil {
			t.Error("write auth response: ", err)
			return
		}
		if _, err := socks5.ReadRequest(reader); err != nil {
			t.Error("read request: ", err)
			return
		}
		if err := socks5.WriteResponse(conn, socks5.Response{ReplyCode: socks5.ReplyCodeSuccess, Bind: M.ParseSocksaddr("127.0.0.1:1")}); err != nil {
			t.Error("write response: ", err)
			return
		}
		after(conn, reader)
	}
}

var handshakeTestDestination = M.ParseSocksaddr("example.com:443")

func TestLxSOCKSHandshakeSilentServerTimesOut(t *testing.T) {
	const bound = 300 * time.Millisecond
	setHandshakeTimeout(t, bound)
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		t.Run(network, func(t *testing.T) {
			closed := make(chan struct{})
			listener, _ := acceptOne(t, silent(closed))
			out := newSOCKSOutbound(t, listener)

			start := time.Now()
			var err error
			if network == N.NetworkTCP {
				var conn net.Conn
				conn, err = out.DialContext(context.Background(), network, handshakeTestDestination)
				if conn != nil {
					conn.Close()
				}
			} else {
				var packetConn net.PacketConn
				packetConn, err = out.ListenPacket(context.Background(), handshakeTestDestination)
				if packetConn != nil {
					packetConn.Close()
				}
			}
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("handshake with a silent server succeeded")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want a handshake timeout, got %v", err)
			}
			if elapsed < bound || elapsed > bound+time.Second {
				t.Fatalf("returned after %v, want about %v", elapsed, bound)
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("the server connection stayed open after the timeout")
			}
		})
	}
}

func TestLxSOCKSHandshakeCancelReturnsImmediately(t *testing.T) {
	// The bound is far away: only the cancellation can end this handshake.
	setHandshakeTimeout(t, time.Minute)
	closed := make(chan struct{})
	listener, _ := acceptOne(t, silent(closed))
	out := newSOCKSOutbound(t, listener)

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		conn, err := out.DialContext(ctx, N.NetworkTCP, handshakeTestDestination)
		if conn != nil {
			conn.Close()
		}
		returned <- err
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-returned:
		t.Fatalf("handshake returned before the cancellation: %v", err)
	default:
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if elapsed := time.Since(cancelled); elapsed > 500*time.Millisecond {
			t.Fatalf("returned %v after the cancellation", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handshake ignored the cancellation")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the server connection stayed open after the cancellation")
	}
}

func TestLxSOCKSHandshakeEarlierContextDeadlineWins(t *testing.T) {
	setHandshakeTimeout(t, time.Minute)
	closed := make(chan struct{})
	listener, _ := acceptOne(t, silent(closed))
	out := newSOCKSOutbound(t, listener)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	conn, err := out.DialContext(ctx, N.NetworkTCP, handshakeTestDestination)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("returned after %v, the caller's deadline was 200ms", elapsed)
	}
}

// TestLxSOCKSHandshakeSuccessKeepsTunnel: an ordinary handshake still works, and
// once it has, neither the timer nor the caller's later cancellation touches the
// tunnel — a read that waits well past the bound still gets its data.
func TestLxSOCKSHandshakeSuccessKeepsTunnel(t *testing.T) {
	const bound = 200 * time.Millisecond
	setHandshakeTimeout(t, bound)
	payload := []byte("late reply")
	listener, _ := acceptOne(t, serveSOCKS5Connect(t, func(conn net.Conn, reader *bufio.Reader) {
		time.Sleep(3 * bound)
		_, _ = conn.Write(payload)
		_, _ = io.Copy(io.Discard, reader)
	}))
	out := newSOCKSOutbound(t, listener)

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := out.DialContext(ctx, N.NetworkTCP, handshakeTestDestination)
	if err != nil {
		t.Fatal("handshake failed: ", err)
	}
	defer conn.Close()
	// A pooled consumer cancels the dial context as soon as the dial returns.
	cancel()
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal("the established tunnel was cut: ", err)
	}
	if string(buffer) != string(payload) {
		t.Fatalf("read %q, want %q", buffer, payload)
	}
}
