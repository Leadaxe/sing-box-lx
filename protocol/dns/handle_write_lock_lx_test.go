package dns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-mux"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	mDNS "github.com/miekg/dns"
)

const lxTestVLESSUser = "b831381d-6324-4d53-ad4f-8cda48b30811"

// Answers every query on its own goroutine, like a real router whose
// upstream exchanges complete concurrently.
type lxAsyncRouter struct {
	adapter.DNSRouter
}

func (r *lxAsyncRouter) ExchangeAsync(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions, callback func(response *mDNS.Msg, err error)) {
	go func() {
		response := new(mDNS.Msg)
		response.SetReply(message)
		callback(response, nil)
	}()
}

// Serves stream DNS on proxied conns through the real dns outbound, so the
// test also fails when its NewConnection stops serializing the writes.
type lxDNSHandler struct{}

func (h *lxDNSHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	outbound := &Outbound{router: &lxAsyncRouter{}}
	go outbound.NewConnection(ctx, conn, adapter.InboundContext{}, nil)
}

func (h *lxDNSHandler) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	conn.Close()
}

func lxServe(t *testing.T, handle func(net.Conn)) net.Listener {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go handle(conn)
		}
	}()
	return listener
}

type lxListenerDialer struct {
	address string
}

func (d lxListenerDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return net.Dial("tcp", d.address)
}

func (d lxListenerDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

// Concurrent responses on a fresh proxied conn must not duplicate the
// server's protocol response header, which the client reads as a zero
// DNS length prefix ("dns: short read").
func TestLxStreamDNSConcurrentResponsesOnVLESSConn(t *testing.T) {
	service := vless.NewService[int](logger.NOP(), &lxDNSHandler{})
	service.UpdateUsers([]int{0}, []string{lxTestVLESSUser}, []string{""})
	proxyListener := lxServe(t, func(conn net.Conn) {
		if service.NewConnection(context.Background(), conn, M.SocksaddrFromNet(conn.RemoteAddr()), nil) != nil {
			conn.Close()
		}
	})
	client, err := vless.NewClient(lxTestVLESSUser, "", logger.NOP())
	if err != nil {
		t.Fatal(err)
	}
	lxRunPipelinedQueries(t, func() (net.Conn, error) {
		conn, dialErr := net.Dial("tcp", proxyListener.Addr().String())
		if dialErr != nil {
			return nil, dialErr
		}
		return client.DialEarlyConn(conn, M.ParseSocksaddr("8.8.4.4:53"))
	})
}

func TestLxStreamDNSConcurrentResponsesOnMuxConn(t *testing.T) {
	service, err := mux.NewService(mux.ServiceOptions{
		NewStreamContext: func(ctx context.Context, _ net.Conn) context.Context { return ctx },
		Logger:           logger.NOP(),
		HandlerEx:        &lxDNSHandler{},
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyListener := lxServe(t, func(conn net.Conn) {
		service.NewConnectionEx(context.Background(), conn, M.SocksaddrFromNet(conn.RemoteAddr()), M.Socksaddr{}, nil)
	})
	client, err := mux.NewClient(mux.Options{
		Dialer:     lxListenerDialer{address: proxyListener.Addr().String()},
		Logger:     logger.NOP(),
		Protocol:   "h2mux",
		MaxStreams: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	lxRunPipelinedQueries(t, func() (net.Conn, error) {
		return client.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("8.8.4.4:53"))
	})
}

// The client side writes from a single goroutine, so any corruption comes
// from the server's response path.
func lxRunPipelinedQueries(t *testing.T, dial func() (net.Conn, error)) {
	const (
		rounds  = 50
		queries = 16
	)
	failures := make(map[string]int)
	for range rounds {
		conn, err := dial()
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		for i := range queries {
			message := new(mDNS.Msg)
			message.SetQuestion("example.com.", mDNS.TypeA)
			if err = transport.WriteMessage(conn, uint16(i+1), message); err != nil {
				break
			}
		}
		for range queries {
			if err != nil {
				break
			}
			_, err = transport.ReadMessage(conn)
		}
		if err != nil {
			failures[err.Error()]++
		}
		conn.Close()
	}
	if len(failures) > 0 {
		t.Fatalf("%v of %d rounds failed: %v", lxSum(failures), rounds, failures)
	}
}

func lxSum(counts map[string]int) int {
	var total int
	for _, n := range counts {
		total += n
	}
	return total
}
