package wireguard

// lx: SPEC 153 — the masquerade burst by path, one case per row of the
// interaction table, plus the bind's own path classification.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

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

func pathKernel(kernel bool) func() (bool, error) {
	return func() (bool, error) { return kernel, nil }
}

func TestHandshakeBurstFilterTable(t *testing.T) {
	t.Parallel()
	quicBurst := [][]byte{burstQUICInitial, burstJunk}
	cases := []struct {
		name       string
		kernel     bool
		masquerade string
		sniff      string
		want       [][]byte
	}{
		{"direct / direct / off", true, option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyOff, quicBurst},
		{"direct / all / detour", true, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyDetour, quicBurst},
		{"direct / direct / direct", true, option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyDirect, append([][]byte{burstDecoyByte}, quicBurst...)},
		{"direct / all / all", true, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyAll, append([][]byte{burstDecoyByte}, quicBurst...)},
		{"detour / direct / all", false, option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyAll, nil},
		{"detour / direct / off", false, option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyOff, nil},
		{"detour / all / off", false, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyOff, quicBurst},
		{"detour / all / direct", false, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyDirect, quicBurst},
		{"detour / all / detour", false, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyDetour, append([][]byte{burstDecoyByte}, quicBurst...)},
		{"detour / all / all", false, option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyAll, append([][]byte{burstDecoyByte}, quicBurst...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := newHandshakeBurstFilter(tc.masquerade, tc.sniff, pathKernel(tc.kernel), nil)
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

// The default (masquerade direct, decoy off) still installs a filter: it
// strips over a detour. Only "all" with the decoy off has nothing to do.
func TestHandshakeBurstFilterInstalledOnlyWhenItCanAct(t *testing.T) {
	t.Parallel()
	require.NotNil(t, newHandshakeBurstFilter(option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyOff, pathKernel(true), nil))
	require.NotNil(t, newHandshakeBurstFilter(option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyDetour, pathKernel(true), nil))
	require.Nil(t, newHandshakeBurstFilter(option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyOff, pathKernel(true), nil))
	require.Nil(t, newHandshakeBurstFilter(option.LXAWGMasqueradeAll, "", pathKernel(true), nil))
}

// Only a burst that starts QUIC-shaped gets the byte: a dns/stun/sip decoy or
// plain junk is not what the sniffer is after. An empty burst stays empty.
func TestHandshakeBurstDecoyOnlyAheadOfQUIC(t *testing.T) {
	t.Parallel()
	filter := newHandshakeBurstFilter(option.LXAWGMasqueradeAll, option.LXQUICSniffDecoyAll, pathKernel(false), nil)
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
	filter := newHandshakeBurstFilter(option.LXAWGMasqueradeDirect, option.LXQUICSniffDecoyAll, func() (bool, error) { return false, errors.New("dial") }, nil)
	burst := [][]byte{burstQUICInitial}
	require.Equal(t, burst, filter(burst))
}

// pipeDialer stands for a detour: its conns are not kernel sockets.
type pipeDialer struct{}

func (pipeDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
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

func TestClientBindPathIsKernel(t *testing.T) {
	t.Parallel()
	ctx := pause.ContextWithDefaultManager(context.Background())
	target := netip.MustParseAddrPort("127.0.0.1:9")

	kernel := NewClientBind(ctx, nil, N.SystemDialer, true, target, [3]uint8{})
	_, _, err := kernel.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { kernel.Close() })
	isKernel, err := kernel.PathIsKernel()
	require.NoError(t, err)
	require.True(t, isKernel, "a system dialer yields a kernel socket")

	detour := NewClientBind(ctx, nil, pipeDialer{}, true, target, [3]uint8{})
	_, _, err = detour.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { detour.Close() })
	isKernel, err = detour.PathIsKernel()
	require.NoError(t, err)
	require.False(t, isKernel, "a pipe-carried conn is a detour")

	listen := NewClientBind(ctx, nil, N.SystemDialer, false, netip.AddrPort{}, [3]uint8{})
	_, _, err = listen.Open(0)
	require.NoError(t, err)
	t.Cleanup(func() { listen.Close() })
	isKernel, err = listen.PathIsKernel()
	require.NoError(t, err)
	require.True(t, isKernel, "ListenPacket on the system dialer is a kernel socket")

	require.True(t, bytes.Equal(lxdatagram.SniffDecoy(), []byte{0x00}))
}
