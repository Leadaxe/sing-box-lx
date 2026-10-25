package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	D "github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type downloadBootstrapNetwork struct{ adapter.NetworkManager }

func (downloadBootstrapNetwork) DefaultOptions() adapter.NetworkOptions {
	return adapter.NetworkOptions{DomainResolver: "bootstrap-fixture"}
}

type downloadBootstrapTransport struct{ adapter.DNSTransport }
type downloadBootstrapManager struct {
	adapter.DNSTransportManager
	fixture adapter.DNSTransport
}

func (m downloadBootstrapManager) Transport(tag string) (adapter.DNSTransport, bool) {
	return m.fixture, tag == "bootstrap-fixture"
}

type downloadBootstrapRouter struct {
	adapter.DNSRouter
	fixture adapter.DNSTransport
	access  sync.Mutex
	seen    map[string]int
}

func (r *downloadBootstrapRouter) Lookup(_ context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	if options.Transport != r.fixture {
		return nil, fmt.Errorf("wrong bootstrap transport")
	}
	if domain != "uplink.invalid" && domain != "downlink.invalid" {
		return nil, fmt.Errorf("unexpected DNS name")
	}
	r.access.Lock()
	r.seen[domain]++
	r.access.Unlock()
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// A direct dialer created for an IP uplink cannot resolve a domain download leg.
// Both names must use the core bootstrap path, not the OS or tunnel resolver.
func TestIndependentDownloadBootstrapDNS(t *testing.T) {
	for _, uploadDomain := range []bool{false, true} {
		t.Run(fmt.Sprintf("domain-upload-%t", uploadDomain), func(t *testing.T) {
			fixture := &downloadBootstrapTransport{}
			router := &downloadBootstrapRouter{fixture: fixture, seen: make(map[string]int)}
			ctx := service.ContextWith[adapter.DNSRouter](context.Background(), router)
			ctx = service.ContextWith[adapter.DNSTransportManager](ctx, downloadBootstrapManager{fixture: fixture})
			ctx = service.ContextWith[adapter.NetworkManager](ctx, downloadBootstrapNetwork{})
			payloads := make(chan []byte, 1)
			up, _ := downloadTestServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return
				}
				payloads <- body
				w.WriteHeader(http.StatusOK)
			}))
			down, _ := downloadTestServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case payload := <-payloads:
					w.Write(payload)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case <-r.Context().Done():
				}
			}))
			direct, err := D.NewDefault(context.Background(), option.DialerOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var inherited N.Dialer = direct
			upAddress := M.SocksaddrFromNet(up.Listener.Addr())
			if uploadDomain {
				upAddress = M.Socksaddr{Fqdn: "uplink.invalid", Port: upAddress.Port}
				inherited = D.NewResolveDialer(ctx, direct, true, "bootstrap-fixture", adapter.DNSQueryOptions{Transport: fixture}, 0)
			}
			transport, err := NewClient(ctx, inherited, upAddress, option.V2RayXHTTPOptions{Mode: modePacketUp,
				DownloadSettings: &option.V2RayXHTTPDownloadOptions{ServerOptions: option.ServerOptions{
					Server: "downlink.invalid", ServerPort: M.SocksaddrFromNet(down.Listener.Addr()).Port}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			client := transport.(*Client)
			defer client.Close()
			dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			conn, err := client.DialContext(dialCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = conn.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 5)
			if _, err = io.ReadFull(conn, body); err != nil || string(body) != "hello" {
				t.Fatalf("bootstrap echo %q: %v", body, err)
			}
			conn.Close()
			assertNoDownloadLeases(t, client)
			router.access.Lock()
			defer router.access.Unlock()
			if router.seen["downlink.invalid"] == 0 || uploadDomain && router.seen["uplink.invalid"] == 0 {
				t.Fatalf("missing core bootstrap lookup: %v", router.seen)
			}
		})
	}
}
