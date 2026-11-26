package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	stdHTTP "net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

// SPEC 161. Since the sync of 2026-11-26 the CONNECT bound is upstream's own
// (sing 963f5b53e, HandshakeTimeout = C.TCPTimeout set by NewOutbound, started
// after the underlying dial); these tests stay as the guard of that behaviour.
// setHandshakeTimeout shortens the client's private bound so they run fast.
func setHandshakeTimeout(t *testing.T, out adapter.Outbound, timeout time.Duration) {
	t.Helper()
	field := reflect.ValueOf(out.(*Outbound).client).Elem().FieldByName("handshakeTimeout")
	if !field.IsValid() {
		t.Fatal("sing HTTP client has no handshakeTimeout field any more")
	}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(timeout))
}

// TestLxHTTPConnectOutage exercises the real outbound and real TCP sockets. A
// successful TCP dial must not leave CONNECT waiting forever, and a successful
// CONNECT must not leave a timer that later closes the established tunnel.
func TestLxHTTPConnectOutage(t *testing.T) {
	const httpConnectHandshakeTimeoutLx = 500 * time.Millisecond

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
				conn.SetDeadline(time.Now().Add(5 * time.Second))
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
			outboundContext := context.Background()
			options := option.HTTPOutboundOptions{
				ServerOptions: option.ServerOptions{Server: server.AddrString(), ServerPort: server.Port},
			}
			detourContext := make(chan context.Context, 1)
			if mode == "tunnel-survives" {
				options.Detour = "loopback-detour"
				outboundContext = service.ContextWith[adapter.OutboundManager](outboundContext, httpOutageDetourManager{
					detour: httpOutageDetour{dialContext: detourContext, delay: 2 * httpConnectHandshakeTimeoutLx},
				})
			}
			out, err := NewOutbound(outboundContext, nil, log.NewNOPFactory().Logger(), "http-test", options)
			if err != nil {
				t.Fatal(err)
			}
			setHandshakeTimeout(t, out, httpConnectHandshakeTimeoutLx)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, httpConnectHandshakeTimeoutLx/2)
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
			case <-time.After(5 * time.Second):
				select {
				case err := <-peerDone:
					t.Fatal("proxy did not receive CONNECT: ", err)
				default:
				}
				t.Fatal("proxy did not receive CONNECT")
			}
			if mode == "cancel" {
				cancel()
			}
			var response dialResult
			select {
			case response = <-result:
			case <-time.After(httpConnectHandshakeTimeoutLx + 2*time.Second):
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
				select {
				case underlying := <-detourContext:
					if _, hasDeadline := underlying.Deadline(); hasDeadline || underlying.Done() != ctx.Done() {
						t.Fatal("HTTP outbound changed the detour's deadline or cancellation")
					}
				default:
					t.Fatal("HTTP outbound did not dial via the detour")
				}
				cancel() // The HTTP client's cancellation watcher must be disarmed.
				response.conn.SetDeadline(time.Now().Add(3 * time.Second))
				greeting := make([]byte, 5)
				if _, err := io.ReadFull(response.conn, greeting); err != nil || string(greeting) != "hello" {
					t.Fatalf("buffered tunnel bytes: %q, %v", greeting, err)
				}
				time.Sleep(2 * httpConnectHandshakeTimeoutLx)
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
				switch mode {
				case "silent", "partial":
					if !errors.Is(response.err, context.DeadlineExceeded) {
						t.Fatal("expected a CONNECT timeout, got: ", response.err)
					}
				case "cancel":
					if !errors.Is(response.err, context.Canceled) {
						t.Fatal("caller cancellation was lost: ", response.err)
					}
				case "deadline":
					if !errors.Is(response.err, context.DeadlineExceeded) {
						t.Fatal("caller deadline was lost: ", response.err)
					}
				case "503":
					if errors.Is(response.err, context.DeadlineExceeded) || !strings.Contains(response.err.Error(), "503 Service Unavailable") {
						t.Fatal("expected the proxy's 503 error, got: ", response.err)
					}
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

type httpOutageDetourManager struct {
	adapter.OutboundManager
	detour httpOutageDetour
}

func (m httpOutageDetourManager) Outbound(tag string) (adapter.Outbound, bool) {
	return m.detour, tag == m.detour.Tag()
}

type httpOutageDetour struct {
	adapter.Outbound
	dialContext chan<- context.Context
	delay       time.Duration
}

func (d httpOutageDetour) Tag() string { return "loopback-detour" }

func (d httpOutageDetour) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.dialContext <- ctx
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, destination.String())
}

// TestLxHTTPConnectWatchdogRace answers CONNECT right at the watchdog's expiry.
// Whichever side wins, the caller gets either a working tunnel with its
// buffered bytes or a timeout and no connection, never a closed tunnel.
func TestLxHTTPConnectWatchdogRace(t *testing.T) {
	const timeout = 20 * time.Millisecond

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		// Spread the answers a few milliseconds around the expiry.
		for delay := timeout - 4*time.Millisecond; ; delay += 125 * time.Microsecond {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				if _, err := stdHTTP.ReadRequest(reader); err != nil {
					return
				}
				time.Sleep(delay)
				if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nhello"); err != nil {
					return
				}
				io.Copy(conn, reader)
			}()
		}
	}()

	server := M.ParseSocksaddr(listener.Addr().String())
	out, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "http-test", option.HTTPOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server.AddrString(), ServerPort: server.Port},
	})
	if err != nil {
		t.Fatal(err)
	}
	setHandshakeTimeout(t, out, timeout)
	var (
		group     sync.WaitGroup
		tunnels   atomic.Int32
		timeouts  atomic.Int32
		failures  = make(chan string, 64)
		attempts  = 64
		reportErr = func(message string) {
			select {
			case failures <- message:
			default:
			}
		}
	)
	for i := 0; i < attempts; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			conn, err := out.DialContext(context.Background(), "tcp", M.ParseSocksaddr("198.51.100.42:443"))
			if err != nil {
				if conn != nil {
					reportErr("error returned together with a connection")
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					reportErr("unexpected error: " + err.Error())
				}
				timeouts.Add(1)
				return
			}
			defer conn.Close()
			tunnels.Add(1)
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			greeting := make([]byte, 5)
			if _, err := io.ReadFull(conn, greeting); err != nil || string(greeting) != "hello" {
				reportErr("returned tunnel lost its buffered bytes or was closed by the watchdog")
				return
			}
			time.Sleep(2 * timeout)
			if _, err := io.WriteString(conn, "alive"); err != nil {
				reportErr("returned tunnel was closed after the handshake: " + err.Error())
				return
			}
			if _, err := io.ReadFull(conn, greeting); err != nil || string(greeting) != "alive" {
				reportErr("returned tunnel does not carry traffic")
			}
		}()
	}
	group.Wait()
	close(failures)
	for message := range failures {
		t.Error(message)
	}
	t.Logf("tunnels: %d, timeouts: %d", tunnels.Load(), timeouts.Load())
}
