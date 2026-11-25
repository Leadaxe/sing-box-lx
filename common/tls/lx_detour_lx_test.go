package tls

// lx: SPEC 165 — the lx.detour.strip filters on the TLS client handshake
// (ALGORITHM С1–С5). The kernel-socket check sees a loopback TCP conn as a
// kernel socket and a net.Pipe as a stream inside a tunnel.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	tf "github.com/sagernet/sing-box/common/tlsfragment"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	aTLS "github.com/sagernet/sing/common/tls"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

// lxDetourContext is the build context of node "node" in a box whose
// lx.detour has strip and whose strip set holds the nodes in covered.
func lxDetourContext(strip option.LXDetourStripResolved, covered ...string) context.Context {
	nodes := make(map[string]bool)
	for _, tag := range covered {
		nodes[tag] = true
	}
	resolved := &option.LXResolved{Detour: option.LXDetourResolved{MTUAlign: "clamp", Strip: strip}, DetourNodes: nodes}
	ctx := adapter.ContextWithOutboundTag(context.Background(), "node")
	return service.ContextWithPtr(ctx, resolved)
}

var lxDetourCatalog = option.LXDetourStripResolved{TLSFragment: true, TLSSpoof: true, TLSKernel: true, TLSEngine: true}

func lxDetourSTDConfig(t *testing.T, ctx context.Context, fragment bool, recordFragment *bool) Config {
	t.Helper()
	config, err := NewClientWithOptions(ClientOptions{
		Context:       ctx,
		Logger:        logger.NOP(),
		ServerAddress: "127.0.0.1",
		Options: option.OutboundTLSOptions{
			Enabled: true, ServerName: lxGuardServerName, Insecure: true,
			Fragment: fragment, RecordFragment: recordFragment,
		},
	})
	require.NoError(t, err)
	return config
}

func lxDetourKernelConn(t *testing.T) net.Conn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		if conn, err := listener.Accept(); err == nil {
			t.Cleanup(func() { conn.Close() })
		}
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func lxDetourTunnelConn(t *testing.T) net.Conn {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	return client
}

func TestLxDetourС2FragmentDroppedInTunnel(t *testing.T) {
	ctx := lxDetourContext(lxDetourCatalog, "node")
	config := lxDetourSTDConfig(t, ctx, true, nil)
	tunnel := lxDetourTunnelConn(t)
	shaped := sniffGuardConnLx(context.Background(), tunnel, config)
	guarded, isShaped := shaped.(*sniffGuardConn)
	require.True(t, isShaped, "the engine must skip its own split")
	require.Same(t, tunnel, guarded.Conn, "no split, no record split")
	require.NotNil(t, guarded.kernelOnly, "the spoof/kTLS mark rides on the shaped conn")

	// A kernel socket keeps the node's split (the engine applies it).
	kernel := lxDetourKernelConn(t)
	require.Same(t, kernel, sniffGuardConnLx(context.Background(), kernel, config))
}

func TestLxDetourС2RecordFragmentKept(t *testing.T) {
	ctx := lxDetourContext(option.LXDetourStripResolved{TLSFragment: true}, "node")
	config := lxDetourSTDConfig(t, ctx, true, new(true))
	shaped := sniffGuardConnLx(context.Background(), lxDetourTunnelConn(t), config).(*sniffGuardConn)
	_, isFragment := shaped.Conn.(*tf.Conn)
	require.True(t, isFragment, "record_fragment stays")
}

func TestLxDetourС1NotCovered(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no detour or excepted": lxDetourContext(lxDetourCatalog, "other"),
		"filter off":            lxDetourContext(option.LXDetourStripResolved{TLSSpoof: true}, "node"),
		"no lx values":          context.Background(),
	} {
		config := lxDetourSTDConfig(t, ctx, true, nil)
		tunnel := lxDetourTunnelConn(t)
		shaped := sniffGuardConnLx(context.Background(), tunnel, config)
		if guarded, isShaped := shaped.(*sniffGuardConn); isShaped {
			t.Fatalf("%s: the split must stay, got %T", name, guarded.Conn)
		}
	}
}

// С3 and its note: the guard shapes a handshake whose split the filter
// dropped, and its form stays.
func TestLxDetourС3GuardWins(t *testing.T) {
	node := lxGuardNode(t, option.LXSniffGuardTLSFragment, option.LXSniffGuardSuspect, false)
	config := lxDetourSTDConfig(t, lxDetourContext(lxDetourCatalog, "node"), true, nil)
	var shaped net.Conn
	ctx, record := adapter.ContextWithCarrierRecord(context.Background())
	record.Type, record.Tag = "vless", "relay"
	_ = node.Dial(ctx, func(ctx context.Context) error {
		shaped = sniffGuardConnLx(ctx, lxDetourTunnelConn(t), config)
		return nil
	})
	guarded, isShaped := shaped.(*sniffGuardConn)
	require.True(t, isShaped)
	_, isFragment := guarded.Conn.(*tf.Conn)
	require.True(t, isFragment, "И7: the guard's split is not undone by the filter")
}

func TestLxDetourSpoofAndKernel(t *testing.T) {
	ctx := lxDetourContext(lxDetourCatalog, "node")
	config := lxDetourSTDConfig(t, ctx, false, nil)
	marked := sniffGuardConnLx(context.Background(), lxDetourTunnelConn(t), config)
	// The engine's own wrappers sit on top; the mark is found under them.
	wrapped := tf.NewConn(marked, context.Background(), false, true, time.Millisecond)
	require.True(t, skipSpoofLx(wrapped, "decoy.example"), "tls.spoof")
	require.False(t, skipSpoofLx(wrapped, ""), "no spoof configured")
	require.True(t, skipKTLSLx(wrapped), "tls.kernel")

	only := lxDetourContext(option.LXDetourStripResolved{TLSSpoof: true}, "node")
	marked = sniffGuardConnLx(context.Background(), lxDetourTunnelConn(t), lxDetourSTDConfig(t, only, false, nil))
	require.True(t, skipSpoofLx(marked, "decoy.example"))
	require.False(t, skipKTLSLx(marked), "С5: tls.kernel off keeps upstream kTLS")

	kernel := lxDetourKernelConn(t)
	require.False(t, skipSpoofLx(sniffGuardConnLx(context.Background(), kernel, config), "decoy.example"), "Т6б: a kernel socket keeps the spoof")
}

// lxDetourFakeConfig records the handshake it was asked to do.
type lxDetourFakeConfig struct {
	Config
	name string
	used *string
}

func (c *lxDetourFakeConfig) HandshakeTimeout() time.Duration { return 0 }
func (c *lxDetourFakeConfig) Clone() Config                   { return c }
func (c *lxDetourFakeConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	*c.used = c.name
	return nil, nil
}

func TestLxDetourEngine(t *testing.T) {
	var used string
	apple := &lxDetourFakeConfig{name: "apple", used: &used}
	fallback := &lxDetourFakeConfig{name: "go", used: &used}
	config := &lxAppleConfig{Config: apple, fallback: fallback, ctx: lxDetourContext(lxDetourCatalog, "node")}
	_, _ = config.ClientHandshake(context.Background(), lxDetourTunnelConn(t))
	require.Equal(t, "go", used, "Т6г: apple without a kernel socket hands over to go")
	_, _ = config.ClientHandshake(context.Background(), lxDetourKernelConn(t))
	require.Equal(t, "apple", used, "a kernel socket keeps apple")
	config.ctx = lxDetourContext(option.LXDetourStripResolved{TLSFragment: true}, "node")
	_, _ = config.ClientHandshake(context.Background(), lxDetourTunnelConn(t))
	require.Equal(t, "apple", used, "С5: tls.engine off keeps apple")
}

var _ = lxsniffguard.ProtoTLS
