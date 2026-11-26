package dialer

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// cellRouter is a DNS router whose transport detour is a per-connection group:
// the group records its pick into whatever cell the lookup context carries.
type cellRouter struct {
	adapter.DNSRouter
	seen *adapter.ActualOutboundLx
}

func (r *cellRouter) Lookup(ctx context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	if metadata := adapter.ContextFrom(ctx); metadata != nil {
		r.seen = metadata.ActualOutboundLx
	}
	adapter.RecordPickLx(ctx, "rr", &funcOutbound{tag: "dns-node"})
	return nil, errors.New("no address")
}

// lx: SPEC 138 — the lookup of a node's server address runs on the
// connection's context, and its DNS transport may dial through the same
// round_robin group: that dial must not overwrite the connection's pick.
func TestLookupHidesActualCell_LX(t *testing.T) {
	router := &cellRouter{}
	resolver := &resolveDialer{router: router}
	cell := new(adapter.ActualOutboundLx)
	cell.Pick("rr", &funcOutbound{tag: "node"})
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Destination: M.ParseSocksaddr("example.org:443"), ActualOutboundLx: cell})
	_, _ = resolver.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("node.example.com", 443))
	if router.seen != nil {
		t.Fatal("the lookup must not see the connection's cell")
	}
	if got := cell.Load().Picked("rr"); got == nil || got.Tag() != "node" {
		t.Fatalf("the connection's pick must survive the lookup, got %v", got)
	}
	if adapter.ContextFrom(ctx).ActualOutboundLx != cell {
		t.Fatal("the connection's own metadata keeps its cell")
	}
}
