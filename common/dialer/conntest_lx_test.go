//go:build with_lx_command

package dialer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func attrOf(stage lxtrace.StageSnapshot, key string) (string, bool) {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return "", false
}

func TestConnTestDefaultDialerTCPConnectStage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			conn.Close()
		}
	}()
	defaultDialer, err := NewDefault(context.Background(), option.DialerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace := lxtrace.New(lxtrace.Options{Depth: lxtrace.DepthConnect})
	ctx := lxtrace.With(context.Background(), trace)
	address := M.SocksaddrFromNet(listener.Addr())
	conn, err := defaultDialer.DialContext(ctx, N.NetworkTCP, address)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	summary := trace.Finish(nil)
	if len(summary.Stages) != 1 {
		t.Fatalf("want 1 stage, got %d: %+v", len(summary.Stages), summary.Stages)
	}
	stage := summary.Stages[0]
	if stage.Kind != "tcp_connect" || stage.Subject != address.String() || stage.Result != lxtrace.OK {
		t.Fatalf("unexpected stage %+v", stage)
	}
	if local, loaded := attrOf(stage, "local"); !loaded || local == "" {
		t.Fatalf("missing local attr: %+v", stage.Attrs)
	}
	if summary.RootCause != 0 {
		t.Fatalf("root cause on success: %d", summary.RootCause)
	}
}

// cycleRouter resolves a domain by dialing it again through the same
// resolveDialer: a DNS server whose detour needs the very domain it resolves.
type cycleRouter struct {
	adapter.DNSRouter
	dialer *resolveDialer
}

func (r *cycleRouter) Lookup(ctx context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	conn, err := r.dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort(domain, 853))
	if err != nil {
		return nil, err
	}
	conn.Close()
	return nil, errors.New("unreachable")
}

func TestConnTestResolveCycle(t *testing.T) {
	resolver := &resolveDialer{}
	resolver.router = &cycleRouter{dialer: resolver}
	trace := lxtrace.New(lxtrace.Options{})
	ctx := lxtrace.With(context.Background(), trace)
	_, err := resolver.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("dns.example.com", 443))
	if !errors.Is(err, lxtrace.ErrResolveCycle) {
		t.Fatalf("want ErrResolveCycle, got %v", err)
	}
	summary := trace.Finish(nil)
	if len(summary.Stages) != 2 {
		t.Fatalf("want 2 lookup stages, got %+v", summary.Stages)
	}
	outer, inner := summary.Stages[0], summary.Stages[1]
	if outer.Kind != "lookup" || inner.Kind != "lookup" || inner.Parent != outer.ID {
		t.Fatalf("unexpected stages %+v", summary.Stages)
	}
	if resolverName, _ := attrOf(inner, "resolver"); resolverName != "default_domain_resolver" {
		t.Fatalf("unexpected resolver %q", resolverName)
	}
	if cycleWith, _ := attrOf(inner, "cycle_with"); cycleWith == "" {
		t.Fatalf("missing cycle_with: %+v", inner.Attrs)
	}
	if summary.RootCause != inner.ID {
		t.Fatalf("root cause %d, want %d", summary.RootCause, inner.ID)
	}
}
