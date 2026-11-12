package wireguard

// lx: SPEC 151 — the sniff decoy ahead of the masquerade burst by policy and
// path, plus the bind's own path classification.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxdatagram"
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

func pathOf(path lxdatagram.Path) func() (lxdatagram.Path, error) {
	return func() (lxdatagram.Path, error) { return path, nil }
}

func burstPolicy(mode string, outboundTypes, detourTypes []string) option.LXQUICSniffDecoyResolved {
	if outboundTypes == nil {
		outboundTypes = []string{"wireguard"}
	}
	if detourTypes == nil {
		detourTypes = []string{"vless"}
	}
	return option.LXQUICSniffDecoyResolved{Mode: mode, OutboundTypes: outboundTypes, DetourTypes: detourTypes, Retry: true}
}

// The byte by mode and path: the direct path (a kernel socket), a relay of a
// type in detour_type, a relay of another type. The burst itself is never
// stripped.
func TestHandshakeBurstFilterTable(t *testing.T) {
	t.Parallel()
	quicBurst := [][]byte{burstQUICInitial, burstJunk}
	withByte := append([][]byte{burstDecoyByte}, quicBurst...)
	kernel := lxdatagram.Path{Kernel: true}
	vless := lxdatagram.Path{Carrier: "vless"}
	socks := lxdatagram.Path{Carrier: "socks"}
	cases := []struct {
		name string
		mode string
		path lxdatagram.Path
		want [][]byte
	}{
		{"off / kernel", option.LXQUICSniffDecoyOff, kernel, quicBurst},
		{"off / vless", option.LXQUICSniffDecoyOff, vless, quicBurst},
		{"direct / kernel", option.LXQUICSniffDecoyDirect, kernel, withByte},
		{"direct / vless", option.LXQUICSniffDecoyDirect, vless, quicBurst},
		{"detour / kernel", option.LXQUICSniffDecoyDetour, kernel, quicBurst},
		{"detour / vless", option.LXQUICSniffDecoyDetour, vless, withByte},
		{"detour / socks", option.LXQUICSniffDecoyDetour, socks, quicBurst},
		{"detour / unknown carrier", option.LXQUICSniffDecoyDetour, lxdatagram.Path{}, quicBurst},
		{"all / kernel", option.LXQUICSniffDecoyAll, kernel, withByte},
		{"all / vless", option.LXQUICSniffDecoyAll, vless, withByte},
		{"all / socks", option.LXQUICSniffDecoyAll, socks, quicBurst},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := newHandshakeBurstFilter(burstPolicy(tc.mode, nil, nil), pathOf(tc.path), nil)
			var got [][]byte
			if filter == nil {
				got = quicBurst
			} else {
				got = filter(append([][]byte(nil), quicBurst...))
			}
			require.Equal(t, tc.want, got)
		})
	}
}

// A filter is installed only when the policy covers wireguard.
func TestHandshakeBurstFilterInstalledOnlyWhenItCanAct(t *testing.T) {
	t.Parallel()
	require.NotNil(t, newHandshakeBurstFilter(burstPolicy(option.LXQUICSniffDecoyDetour, nil, nil), pathOf(lxdatagram.Path{Kernel: true}), nil))
	require.Nil(t, newHandshakeBurstFilter(burstPolicy(option.LXQUICSniffDecoyOff, nil, nil), pathOf(lxdatagram.Path{Kernel: true}), nil))
	require.Nil(t, newHandshakeBurstFilter(burstPolicy(option.LXQUICSniffDecoyAll, []string{"masque"}, nil), pathOf(lxdatagram.Path{Kernel: true}), nil))
	require.Nil(t, newHandshakeBurstFilter(option.LXQUICSniffDecoyResolved{}, pathOf(lxdatagram.Path{Kernel: true}), nil))
}

// Only a burst that starts QUIC-shaped gets the byte: a dns/stun/sip decoy or
// plain junk is not what the sniffer is after. An empty burst stays empty.
func TestHandshakeBurstDecoyOnlyAheadOfQUIC(t *testing.T) {
	t.Parallel()
	filter := newHandshakeBurstFilter(burstPolicy(option.LXQUICSniffDecoyAll, nil, nil), pathOf(lxdatagram.Path{Carrier: "vless"}), nil)
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
	filter := newHandshakeBurstFilter(burstPolicy(option.LXQUICSniffDecoyAll, nil, nil), func() (lxdatagram.Path, error) { return lxdatagram.Path{}, errors.New("dial") }, nil)
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
	path, err := kernel.Path()
	require.NoError(t, err)
	require.Equal(t, lxdatagram.Path{Kernel: true}, path, "a system dialer yields a kernel socket")

	detour := NewClientBind(ctx, nil, pipeDialer{}, true, target, [3]uint8{})
	_, _, err = detour.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { detour.Close() })
	path, err = detour.Path()
	require.NoError(t, err)
	require.Equal(t, lxdatagram.Path{Carrier: "vless"}, path, "a pipe-carried conn is a detour of the recorded type")

	listen := NewClientBind(ctx, nil, N.SystemDialer, false, netip.AddrPort{}, [3]uint8{})
	_, _, err = listen.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { listen.Close() })
	path, err = listen.Path()
	require.NoError(t, err)
	require.Equal(t, lxdatagram.Path{Kernel: true}, path, "ListenPacket on the system dialer is a kernel socket")

	require.True(t, bytes.Equal(lxdatagram.SniffDecoy(), []byte{0x00}))
}
