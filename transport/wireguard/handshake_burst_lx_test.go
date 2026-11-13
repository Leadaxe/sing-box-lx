package wireguard

// lx: SPEC 060 — the sniff decoy ahead of the masquerade burst by the
// lx.sniff_guard label of the socket's path, plus the bind's own path
// classification.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service/pause"

	"github.com/stretchr/testify/require"
)

var (
	burstQUICInitial = []byte{0xc0, 0x00, 0x00, 0x00, 0x01, 0x08}
	burstDNSQuery    = []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01}
	burstJunk        = []byte{0x55, 0x66, 0x77}
	burstDecoyByte   = lxdatagram.SniffDecoy()
)

// testBurstGuard is a guard whose quic section uses method: the direct path
// and socks relays start clean, vless relays suspect. It covers "awg".
func testBurstGuard(t *testing.T, method string) *lxsniffguard.Guard {
	t.Helper()
	g := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{QUIC: option.LXSniffGuardSectionResolved{
			Method: method,
			Delay:  300 * time.Millisecond,
			DetourTypes: map[string]string{
				option.LXSniffGuardDirect:   option.LXSniffGuardClean,
				"socks":                     option.LXSniffGuardClean,
				option.LXSniffGuardWildcard: option.LXSniffGuardSuspect,
			},
		}},
		Nodes: map[string]lxsniffguard.NodeInfo{"awg": {Type: "wireguard", SNIMismatch: true}},
	})
	require.NotNil(t, g)
	return g
}

var (
	burstDirect = lxsniffguard.Path{Direct: true}
	burstVLESS  = lxsniffguard.Path{CarrierType: "vless", CarrierTag: "relay"}
	burstSOCKS  = lxsniffguard.Path{CarrierType: "socks", CarrierTag: "proxy"}
)

// socketPath is the path func of a bind whose current socket generation is
// generation.
func socketPath(path *lxsniffguard.Path, generation *any) func() (lxsniffguard.Path, any, error) {
	return func() (lxsniffguard.Path, any, error) { return *path, *generation, nil }
}

func pathOf(path lxsniffguard.Path) func() (lxsniffguard.Path, any, error) {
	return func() (lxsniffguard.Path, any, error) { return path, nil, nil }
}

// The byte by the label of the path: a suspect relay gets it, the clean
// direct path and a clean socks relay do not, an unknown path never. The
// burst itself is never stripped.
func TestHandshakeBurstFilterByLabel(t *testing.T) {
	t.Parallel()
	node := testBurstGuard(t, option.LXSniffGuardQUICDecoy).Node("awg")
	quicBurst := [][]byte{burstQUICInitial, burstJunk}
	withByte := append([][]byte{burstDecoyByte}, quicBurst...)
	cases := []struct {
		name string
		path lxsniffguard.Path
		want [][]byte
	}{
		{"direct, clean", burstDirect, quicBurst},
		{"vless relay, suspect", burstVLESS, withByte},
		{"socks relay, clean", burstSOCKS, quicBurst},
		{"unknown path", lxsniffguard.Path{}, quicBurst},
	}
	for _, tc := range cases {
		filter := newHandshakeBurstFilter(node, pathOf(tc.path), nil)
		require.Equal(t, tc.want, filter(append([][]byte(nil), quicBurst...)), tc.name)
	}
}

// A filter is installed only for a covered node; quic.delay does not apply
// to the burst, which is not a QUIC handshake.
func TestHandshakeBurstFilterInstalledOnlyWhenCovered(t *testing.T) {
	t.Parallel()
	g := testBurstGuard(t, option.LXSniffGuardQUICDecoy)
	require.NotNil(t, newHandshakeBurstFilter(g.Node("awg"), pathOf(burstDirect), nil))
	require.Nil(t, newHandshakeBurstFilter(g.Node("other"), pathOf(burstDirect), nil))
	var nilGuard *lxsniffguard.Guard
	require.Nil(t, newHandshakeBurstFilter(nilGuard.Node("awg"), pathOf(burstDirect), nil))

	delay := newHandshakeBurstFilter(testBurstGuard(t, option.LXSniffGuardDelay).Node("awg"), pathOf(burstVLESS), nil)
	burst := [][]byte{burstQUICInitial}
	require.Equal(t, burst, delay(burst), "quic.delay: the burst goes as assembled")
}

// The decision is read once per socket generation: a label that moves while
// the socket lives does not change the flow the relay already judged; a new
// generation reads it again. The handshake outcome is never reported.
func TestHandshakeBurstDecisionPerSocketGeneration(t *testing.T) {
	t.Parallel()
	g := testBurstGuard(t, option.LXSniffGuardQUICDecoy)
	node := g.Node("awg")
	path := burstSOCKS
	var generation any = new(int)
	filter := newHandshakeBurstFilter(node, socketPath(&path, &generation), nil)
	quicBurst := [][]byte{burstQUICInitial}
	require.Equal(t, quicBurst, filter(quicBurst))

	// Another QUIC node learns that the socks relay sniffs.
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		if lxsniffguard.AttemptFromContext(ctx).Decide(lxsniffguard.ProtoQUIC, burstSOCKS, true).Protect {
			return nil
		}
		return errors.New("refused")
	})
	require.NoError(t, err)
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, burstSOCKS).Protect)

	require.Equal(t, quicBurst, filter(quicBurst), "same socket: the decision of its generation")
	generation = new(int)
	require.Equal(t, append([][]byte{burstDecoyByte}, quicBurst...), filter(quicBurst), "a new socket reads the label")
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, burstSOCKS).Protect, "the filter never moves a label")
}

// Only a burst that starts QUIC-shaped gets the byte: a dns/stun/sip decoy or
// plain junk is not what the sniffer is after. An empty burst stays empty.
func TestHandshakeBurstDecoyOnlyAheadOfQUIC(t *testing.T) {
	t.Parallel()
	filter := newHandshakeBurstFilter(testBurstGuard(t, option.LXSniffGuardQUICDecoy).Node("awg"), pathOf(burstVLESS), nil)
	require.Equal(t, [][]byte{burstDNSQuery, burstJunk}, filter([][]byte{burstDNSQuery, burstJunk}))
	require.Equal(t, [][]byte{burstJunk}, filter([][]byte{burstJunk}))
	require.Nil(t, filter(nil))
	require.True(t, quicShaped(burstQUICInitial))
	require.False(t, quicShaped(burstDecoyByte), "the decoy byte itself must not read as QUIC")
	require.False(t, quicShaped([]byte{0x80}), "a long header without the fixed bit is not QUIC")
	require.False(t, quicShaped(nil))
}

// A path that cannot be classified (the dial failed) leaves the burst as
// assembled: the Send right behind reports the fault.
func TestHandshakeBurstFilterKeepsBurstOnPathError(t *testing.T) {
	t.Parallel()
	filter := newHandshakeBurstFilter(testBurstGuard(t, option.LXSniffGuardQUICDecoy).Node("awg"), func() (lxsniffguard.Path, any, error) {
		return lxsniffguard.Path{}, nil, errors.New("dial")
	}, nil)
	burst := [][]byte{burstQUICInitial}
	require.Equal(t, burst, filter(burst))
}

// pipeDialer stands for a detour to a vless node: it records the carrier the
// way the detour dialer does and its conns are not kernel sockets.
type pipeDialer struct{}

type pipeCarrier struct{ adapter.Outbound }

func (pipeCarrier) Type() string { return "vless" }
func (pipeCarrier) Tag() string  { return "relay" }

func (pipeDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	adapter.RecordCarrier(ctx, pipeCarrier{})
	client, server := net.Pipe()
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func (pipeDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("not a packet dialer")
}

func TestClientBindPath(t *testing.T) {
	t.Parallel()
	ctx := pause.ContextWithDefaultManager(context.Background())
	target := netip.MustParseAddrPort("127.0.0.1:9")

	kernel := NewClientBind(ctx, nil, N.SystemDialer, true, target, [3]uint8{})
	_, _, err := kernel.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { kernel.Close() })
	path, generation, err := kernel.Path()
	require.NoError(t, err)
	require.Equal(t, lxsniffguard.Path{Direct: true}, path, "a system dialer yields a kernel socket")
	_, again, err := kernel.Path()
	require.NoError(t, err)
	require.NotNil(t, generation)
	require.True(t, generation == again, "one socket generation")

	detour := NewClientBind(ctx, nil, pipeDialer{}, true, target, [3]uint8{})
	_, _, err = detour.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { detour.Close() })
	path, _, err = detour.Path()
	require.NoError(t, err)
	require.Equal(t, lxsniffguard.Path{CarrierType: "vless", CarrierTag: "relay"}, path, "a pipe-carried conn is a detour through the recorded relay")

	listen := NewClientBind(ctx, nil, N.SystemDialer, false, netip.AddrPort{}, [3]uint8{})
	_, _, err = listen.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { listen.Close() })
	path, _, err = listen.Path()
	require.NoError(t, err)
	require.Equal(t, lxsniffguard.Path{Direct: true}, path, "ListenPacket on the system dialer is a kernel socket")

	require.True(t, bytes.Equal(lxdatagram.SniffDecoy(), []byte{0x00}))
}
