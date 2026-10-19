package route

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 136 — ReferenceManager.Start must not run the first update() on the
// start path: SetKeepIdleConnections on an unreferenced mux outbound takes the
// sing-mux Client.access mutex, which a URLTest dial started at PostStart holds
// for the whole offerNew(), so Box.Start waited out a remote handshake.

// blockingKeeper is an outbound whose SetKeepIdleConnections blocks until the
// test releases it, like a sing-mux client whose access mutex is held by a dial.
type blockingKeeper struct {
	adapter.Outbound
	tag     string
	entered chan bool
	release chan struct{}
}

func (o *blockingKeeper) Tag() string            { return o.tag }
func (o *blockingKeeper) Type() string           { return "stub" }
func (o *blockingKeeper) Dependencies() []string { return nil }
func (o *blockingKeeper) CloseIdleConnections()  {}
func (o *blockingKeeper) SetKeepIdleConnections(keep bool) {
	o.entered <- keep
	<-o.release
}

type referenceOutboundManager struct {
	adapter.OutboundManager
	outbounds []adapter.Outbound
}

func (m *referenceOutboundManager) Outbounds() []adapter.Outbound { return m.outbounds }
func (m *referenceOutboundManager) Default() adapter.Outbound     { return nil }
func (m *referenceOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range m.outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	return nil, false
}

type referenceEndpointManager struct{ adapter.EndpointManager }

func (m *referenceEndpointManager) Endpoints() []adapter.Endpoint { return nil }

type referenceInboundManager struct{ adapter.InboundManager }

func (m *referenceInboundManager) Inbounds() []adapter.Inbound { return nil }

type referenceServiceManager struct{ adapter.ServiceManager }

func (m *referenceServiceManager) Services() []adapter.Service { return nil }

type referenceTransportManager struct{ adapter.DNSTransportManager }

func (m *referenceTransportManager) Transports() []adapter.DNSTransport            { return nil }
func (m *referenceTransportManager) Transport(string) (adapter.DNSTransport, bool) { return nil, false }
func (m *referenceTransportManager) Default() adapter.DNSTransport                 { return nil }

type referenceNetworkManager struct{ adapter.NetworkManager }

func (m *referenceNetworkManager) DefaultOptions() adapter.NetworkOptions {
	return adapter.NetworkOptions{}
}

func newReferenceTestContext(outbounds ...adapter.Outbound) context.Context {
	ctx := context.Background()
	ctx = service.ContextWith[adapter.OutboundManager](ctx, &referenceOutboundManager{outbounds: outbounds})
	ctx = service.ContextWith[adapter.EndpointManager](ctx, &referenceEndpointManager{})
	ctx = service.ContextWith[adapter.InboundManager](ctx, &referenceInboundManager{})
	ctx = service.ContextWith[adapter.ServiceManager](ctx, &referenceServiceManager{})
	ctx = service.ContextWith[adapter.DNSTransportManager](ctx, &referenceTransportManager{})
	ctx = service.ContextWith[adapter.NetworkManager](ctx, &referenceNetworkManager{})
	return ctx
}

// Start(Started) returns while the unreferenced outbound still blocks inside
// SetKeepIdleConnections(false); the call itself still happens, off the start path.
func TestReferenceManagerStart_doesNotWaitForKeepIdle(t *testing.T) {
	keeper := &blockingKeeper{
		tag:     "mux",
		entered: make(chan bool, 1),
		release: make(chan struct{}),
	}
	ctx := newReferenceTestContext(keeper)
	logger := log.NewNOPFactory().NewLogger("test")
	manager := NewReferenceManager(ctx, logger, option.Options{})
	scope := adapter.NewScope(ctx, logger)
	defer scope.Close()

	started := make(chan error, 1)
	go func() {
		started <- manager.Start(adapter.StartStateStarted, scope)
	}()

	select {
	case keep := <-keeper.entered:
		if keep {
			t.Fatalf("unreferenced outbound must get SetKeepIdleConnections(false), got true")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("SetKeepIdleConnections was never called")
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Start(Started) blocked on SetKeepIdleConnections of an unreferenced outbound")
	}
	close(keeper.release)
}
