package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

var errRefusedLx = errors.New("connection refused")

// fakeNode is an outbound whose handshake goes through `relay` and is broken
// without protection, like a cover-SNI node behind a sniffing relay.
type fakeNode struct {
	tag     string
	mark    bool // the handshake layer marks the attempt
	calls   []bool
	records []*adapter.CarrierRecord
}

func (f *fakeNode) Type() string           { return "vless" }
func (f *fakeNode) Tag() string            { return f.tag }
func (f *fakeNode) Network() []string      { return []string{"tcp", "udp"} }
func (f *fakeNode) Dependencies() []string { return nil }

func (f *fakeNode) handshake(ctx context.Context) error {
	record := adapter.CarrierRecordFromContext(ctx)
	f.records = append(f.records, record)
	adapter.RecordCarrier(ctx, &fakeNode{tag: "relay"})
	path := lxsniffguard.Path{CarrierType: record.Type, CarrierTag: record.Tag}
	decision := lxsniffguard.AttemptFromContext(ctx).Decide(lxsniffguard.ProtoTLS, path, f.mark)
	f.calls = append(f.calls, decision.Protect)
	if !decision.Protect {
		return errRefusedLx
	}
	return nil
}

func (f *fakeNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := f.handshake(ctx); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	server.Close()
	return client, nil
}

func (f *fakeNode) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if err := f.handshake(ctx); err != nil {
		return nil, err
	}
	return &net.UDPConn{}, nil
}

type fakeStartCloseNode struct{ fakeNode }

func (f *fakeStartCloseNode) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (f *fakeStartCloseNode) Close() error                                   { return nil }

type fakeFullNode struct{ fakeNode }

func (f *fakeFullNode) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (f *fakeFullNode) InterfaceUpdated(context.Context)               {}
func (f *fakeFullNode) SetKeepIdleConnections(bool)                    {}
func (f *fakeFullNode) CloseIdleConnections()                          {}
func (f *fakeFullNode) MultiplexEnabled() bool                         { return true }

// fakeFlowNode has a method no variant forwards.
type fakeFlowNode struct{ fakeNode }

func (f *fakeFlowNode) PreMatchFlow(string, netip.Addr) adapter.PreMatchAction { return 0 }

type fakeGroupNode struct{ fakeNode }

func (f *fakeGroupNode) All() []string                              { return nil }
func (f *fakeGroupNode) Selected(string) adapter.Outbound           { return nil }
func (f *fakeGroupNode) AttachConnection(io.Closer) (detach func()) { return func() {} }

func testGuardContext(t *testing.T, tags ...string) context.Context {
	t.Helper()
	section := option.LXSniffGuardSectionResolved{
		Method:      option.LXSniffGuardTLSRecordFragment,
		Delay:       300 * time.Millisecond,
		DetourTypes: map[string]string{option.LXSniffGuardDirect: option.LXSniffGuardClean, option.LXSniffGuardWildcard: option.LXSniffGuardClean},
	}
	nodes := make(map[string]lxsniffguard.NodeInfo)
	for _, tag := range tags {
		nodes[tag] = lxsniffguard.NodeInfo{Type: "vless", SNIMismatch: true}
	}
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy:   option.LXSniffGuardResolved{TLS: section},
		Nodes:    nodes,
		Carriers: map[string]lxsniffguard.CarrierInfo{"relay": {Type: "vless", Fingerprint: "vless|relay|443", Encrypted: true}},
	})
	require.NotNil(t, guard)
	return lxsniffguard.ContextWith(service.ContextWithDefaultRegistry(context.Background()), guard)
}

func wrapForTest(t *testing.T, ctx context.Context, inner adapter.Outbound) adapter.Outbound {
	t.Helper()
	wrapped, err := wrapSniffGuardLx(ctx, log.NewNOPFactory().NewLogger("test"), inner.Tag())(inner, nil)
	require.NoError(t, err)
	return wrapped
}

// A fast failure of a marked attempt is retried in the same dial with the
// opposite decision; each attempt has its own carrier record.
func TestSniffGuardWrapperRetry(t *testing.T) {
	ctx := testGuardContext(t, "node")
	inner := &fakeNode{tag: "node", mark: true}
	wrapped := wrapForTest(t, ctx, inner)
	require.NotSame(t, inner, wrapped)

	conn, err := wrapped.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
	require.NoError(t, err)
	conn.Close()
	require.Equal(t, []bool{false, true}, inner.calls)
	require.Len(t, inner.records, 2)
	require.NotSame(t, inner.records[0], inner.records[1])
	require.Equal(t, "relay", inner.records[1].Tag)

	// The pair moved the relay label to suspect: the next dial goes protected
	// at once, packet dials included.
	inner.calls = nil
	packetConn, err := wrapped.ListenPacket(context.Background(), M.ParseSocksaddr("example.com:443"))
	require.NoError(t, err)
	require.NotNil(t, packetConn)
	require.Equal(t, []bool{true}, inner.calls)
}

// Without the handshake mark a failure is neither counted nor retried.
func TestSniffGuardWrapperNoMarkNoRetry(t *testing.T) {
	ctx := testGuardContext(t, "node")
	inner := &fakeNode{tag: "node", mark: false}
	wrapped := wrapForTest(t, ctx, inner)
	_, err := wrapped.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
	require.ErrorIs(t, err, errRefusedLx)
	require.Equal(t, []bool{false}, inner.calls)
}

// A tag the guard does not cover, and a disabled guard, leave the outbound
// as it is.
func TestSniffGuardWrapperUncovered(t *testing.T) {
	ctx := testGuardContext(t, "node")
	inner := &fakeNode{tag: "other"}
	require.Same(t, adapter.Outbound(inner), wrapForTest(t, ctx, inner))
	off := lxsniffguard.ContextWith(service.ContextWithDefaultRegistry(context.Background()), nil)
	inner = &fakeNode{tag: "node"}
	require.Same(t, adapter.Outbound(inner), wrapForTest(t, off, inner))
	require.Same(t, adapter.Outbound(inner), wrapForTest(t, context.Background(), inner))
}

func implemented(outbound adapter.Outbound) map[string]bool {
	_, lifecycle := outbound.(adapter.Lifecycle)
	_, endpoint := outbound.(adapter.Endpoint)
	_, closer := outbound.(io.Closer)
	_, listener := outbound.(adapter.InterfaceUpdateListener)
	_, keeper := outbound.(adapter.IdleConnectionKeeper)
	_, multiplex := outbound.(adapter.OutboundWithMultiplex)
	_, group := outbound.(adapter.OutboundGroup)
	_, flow := outbound.(adapter.FlowOutbound)
	return map[string]bool{
		"lifecycle": lifecycle, "endpoint": endpoint, "closer": closer, "listener": listener,
		"keeper": keeper, "multiplex": multiplex, "group": group, "flow": flow,
	}
}

// The wrapper exposes exactly the interfaces of the inner outbound; one it
// cannot preserve, and a group, stay unwrapped.
func TestSniffGuardWrapperPreservesInterfaces(t *testing.T) {
	ctx := testGuardContext(t, "node")
	for _, inner := range []adapter.Outbound{
		&fakeNode{tag: "node"},
		&fakeStartCloseNode{fakeNode{tag: "node"}},
		&fakeFullNode{fakeNode{tag: "node"}},
	} {
		wrapped := wrapForTest(t, ctx, inner)
		require.NotSame(t, inner, wrapped, "%T", inner)
		require.Equal(t, implemented(inner), implemented(wrapped), "%T", inner)
		require.True(t, sameMethodSet(reflect.TypeOf(inner), reflect.TypeOf(wrapped)), "%T", inner)
	}
	for _, inner := range []adapter.Outbound{
		&fakeFlowNode{fakeNode{tag: "node"}},
		&fakeGroupNode{fakeNode{tag: "node"}},
	} {
		require.Same(t, inner, wrapForTest(t, ctx, inner), "%T", inner)
	}
}
