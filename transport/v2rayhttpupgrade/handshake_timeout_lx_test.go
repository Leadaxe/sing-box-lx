package v2rayhttpupgrade

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: handshake-timeout — the upgrade request was written and its response read
// with neither a deadline nor a look at ctx: a server that accepted the socket
// and stayed silent held VLESS/VMess/Trojan over httpupgrade in DialContext for
// ever, and even cancelling the dial did not release it. Since the sync of
// 2026-11-26 upstream bounds the exchange itself (27c872d81: ctx plus
// C.TCPTimeout); these tests stay as the guard of that behaviour. The bound is
// a constant there, so the silent-server test waits it out in full.

func upgradeServer(t *testing.T, serve func(conn net.Conn, reader *bufio.Reader)) (*Client, <-chan struct{}) {
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
		serve(conn, bufio.NewReader(conn))
	}()
	client, err := NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr(listener.Addr().String()), option.V2RayHTTPUpgradeOptions{Path: "/up"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client, done
}

// silentAfterRequest reads the upgrade request and never answers.
func silentAfterRequest(conn net.Conn, reader *bufio.Reader) {
	_, _ = io.Copy(io.Discard, reader)
}

func TestLxHTTPUpgradeSilentServerTimesOut(t *testing.T) {
	t.Parallel()
	const bound = C.TCPTimeout
	client, done := upgradeServer(t, silentAfterRequest)

	start := time.Now()
	conn, err := client.DialContext(context.Background())
	elapsed := time.Since(start)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a deadline error, got %v", err)
	}
	if elapsed < bound || elapsed > bound+time.Second {
		t.Fatalf("returned after %v, want about %v", elapsed, bound)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the server connection stayed open after the timeout")
	}
}

func TestLxHTTPUpgradeCancelReturnsImmediately(t *testing.T) {
	t.Parallel()
	client, done := upgradeServer(t, silentAfterRequest)

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		conn, err := client.DialContext(ctx)
		if conn != nil {
			conn.Close()
		}
		returned <- err
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-returned:
		t.Fatalf("upgrade returned before the cancellation: %v", err)
	default:
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("upgrade succeeded although the dial was cancelled")
		}
		if elapsed := time.Since(cancelled); elapsed > 500*time.Millisecond {
			t.Fatalf("returned %v after the cancellation", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade ignored the cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the server connection stayed open after the cancellation")
	}
}

// TestLxHTTPUpgradeSuccessKeepsTunnel: an ordinary upgrade still works, bytes the
// server sent with the response are kept, and cancelling the dial context
// afterwards does not cut the tunnel: a read that waits after the cancellation
// still gets its data.
func TestLxHTTPUpgradeSuccessKeepsTunnel(t *testing.T) {
	t.Parallel()
	const bound = 200 * time.Millisecond
	client, _ := upgradeServer(t, func(conn net.Conn, reader *bufio.Reader) {
		request, err := http.ReadRequest(reader)
		if err != nil {
			t.Error("read upgrade request: ", err)
			return
		}
		if request.URL.Path != "/up" {
			t.Error("unexpected path ", request.URL.Path)
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nearly")
		time.Sleep(3 * bound)
		_, _ = io.WriteString(conn, " late")
		_, _ = io.Copy(io.Discard, reader)
	})

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := client.DialContext(ctx)
	if err != nil {
		t.Fatal("upgrade failed: ", err)
	}
	defer conn.Close()
	cancel()
	buffer := make([]byte, len("early late"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal("the established tunnel was cut: ", err)
	}
	if string(buffer) != "early late" {
		t.Fatalf("read %q", buffer)
	}
}
