package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	stdHTTP "net/http"
	"sync"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

// TestLxHTTPConnectOutage exercises the real outbound and real TCP sockets. A
// successful TCP dial must not leave CONNECT waiting forever, and a successful
// CONNECT must not leave a timer that later closes the established tunnel.
func TestLxHTTPConnectOutage(t *testing.T) {
	for _, mode := range []string{"silent", "partial", "cancel", "deadline", "503", "tunnel-survives"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var access sync.Mutex
			var peer net.Conn
			requested := make(chan struct{})
			peerDone := make(chan error, 1)
			t.Cleanup(func() {
				listener.Close()
				access.Lock()
				if peer != nil {
					peer.Close()
				}
				access.Unlock()
			})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					peerDone <- err
					return
				}
				defer conn.Close()
				access.Lock()
				peer = conn
				access.Unlock()
				conn.SetDeadline(time.Now().Add(C.TCPTimeout + 5*time.Second))
				reader := bufio.NewReader(conn)
				request, err := stdHTTP.ReadRequest(reader)
				if err != nil {
					peerDone <- err
					return
				}
				if request.Method != stdHTTP.MethodConnect || request.Host != "198.51.100.42:443" {
					peerDone <- errors.New("invalid CONNECT request")
					return
				}
				close(requested)
				switch mode {
				case "partial":
					_, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\nX-Incomplete:")
				case "503":
					_, err = io.WriteString(conn, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\n\r\n")
				case "tunnel-survives":
					// Send tunnel bytes with the response, exercising CachedConn too.
					_, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nhello")
				}
				if err == nil {
					if mode == "tunnel-survives" {
						_, err = io.Copy(conn, reader)
					} else {
						_, err = io.Copy(io.Discard, reader)
					}
				}
				peerDone <- err
			}()

			server := M.ParseSocksaddr(listener.Addr().String())
			out, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "http-test", option.HTTPOutboundOptions{
				ServerOptions: option.ServerOptions{Server: server.AddrString(), ServerPort: server.Port},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 250*time.Millisecond)
				defer stop()
			}
			type dialResult struct {
				conn net.Conn
				err  error
			}
			result := make(chan dialResult, 1)
			go func() {
				conn, err := out.DialContext(ctx, "tcp", M.ParseSocksaddr("198.51.100.42:443"))
				result <- dialResult{conn, err}
			}()
			select {
			case <-requested:
			case err := <-peerDone:
				t.Fatal("proxy did not receive CONNECT: ", err)
			case <-time.After(5 * time.Second):
				t.Fatal("proxy did not receive CONNECT")
			}
			if mode == "cancel" {
				cancel()
			}
			budget := C.TCPTimeout + 2*time.Second
			if mode == "cancel" || mode == "deadline" || mode == "503" || mode == "tunnel-survives" {
				budget = 2 * time.Second
			}
			var response dialResult
			select {
			case response = <-result:
			case <-time.After(budget):
				cancel()
				select {
				case response = <-result:
					if response.conn != nil {
						response.conn.Close()
					}
				case <-time.After(time.Second):
				}
				t.Fatal("CONNECT did not finish within its handshake budget")
			}
			if mode == "tunnel-survives" {
				if response.err != nil {
					t.Fatal(response.err)
				}
				defer response.conn.Close()
				cancel() // The HTTP client's cancellation watcher must be disarmed.
				response.conn.SetDeadline(time.Now().Add(C.TCPTimeout + 3*time.Second))
				greeting := make([]byte, 5)
				if _, err := io.ReadFull(response.conn, greeting); err != nil || string(greeting) != "hello" {
					t.Fatalf("buffered tunnel bytes: %q, %v", greeting, err)
				}
				time.Sleep(C.TCPTimeout + 100*time.Millisecond)
				if _, err := io.WriteString(response.conn, "alive"); err != nil {
					t.Fatal("established tunnel closed after handshake: ", err)
				}
				if _, err := io.ReadFull(response.conn, greeting); err != nil || string(greeting) != "alive" {
					t.Fatalf("established tunnel echo: %q, %v", greeting, err)
				}
				response.conn.Close()
			} else {
				if response.conn != nil {
					response.conn.Close()
					t.Fatal("failed handshake returned a connection")
				}
				if response.err == nil {
					t.Fatal("failed handshake returned no error")
				}
				if mode == "cancel" && !errors.Is(response.err, context.Canceled) {
					t.Fatal("caller cancellation was lost: ", response.err)
				}
				if mode == "deadline" && !errors.Is(response.err, context.DeadlineExceeded) {
					t.Fatal("caller deadline was lost: ", response.err)
				}
			}
			select {
			case err := <-peerDone:
				if err != nil {
					t.Fatal("proxy did not observe a clean connection close: ", err)
				}
			case <-time.After(time.Second):
				t.Fatal("proxy connection was retained after handshake finished")
			}
		})
	}
}
