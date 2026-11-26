//go:build with_lx_command

package route

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

// SPEC 166 — since upstream 37fffa325 a failed open erases the history of the
// chain's last element and calls PerformUpdateCheck of every urltest group of
// the chain. Our chain ends at the urltest group (SPEC 140), so a connection
// test that fails would re-select the group it only observes (SPEC 139 §5.7).

type probeURLTestGroup struct {
	adapter.URLTestGroup
	tag    string
	checks atomic.Int32
}

func (g *probeURLTestGroup) Tag() string         { return g.tag }
func (g *probeURLTestGroup) PerformUpdateCheck() { g.checks.Add(1) }

type unreachableDialer struct{}

func (unreachableDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, syscall.EHOSTUNREACH
}

func (unreachableDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, syscall.EHOSTUNREACH
}

func TestLxFailedOpenOfProbeLeavesURLTestGroups(t *testing.T) {
	history := urltest.NewHistoryStorage()
	manager := NewConnectionManager(service.ContextWithPtr(context.Background(), history), log.NewNOPFactory().NewLogger("test"))
	group := &probeURLTestGroup{tag: "auto"}
	history.StoreURLTestHistory("auto", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})
	open := func(ctx context.Context) {
		client, server := net.Pipe()
		defer client.Close()
		manager.NewConnection(ctx, unreachableDialer{}, server, adapter.InboundContext{
			Destination:   M.ParseSocksaddr("192.0.2.1:443"),
			OutboundChain: []adapter.Outbound{group},
		}, nil)
	}

	open(lxtrace.With(context.Background(), lxtrace.New(lxtrace.Options{Probe: true})))
	if group.checks.Load() != 0 || history.LoadURLTestHistory("auto") == nil {
		t.Fatal("a failed probe must leave the urltest groups of its chain alone")
	}

	// Control: a production open reacts as upstream.
	open(context.Background())
	if group.checks.Load() != 1 || history.LoadURLTestHistory("auto") != nil {
		t.Fatalf("a failed production open re-selects (checks=%d) and erases the last element's history", group.checks.Load())
	}
}
