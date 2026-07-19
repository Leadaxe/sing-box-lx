//go:build with_gvisor && with_wireguard && with_awg

// lx: SPEC 028 — end-to-end stand for nested AmneziaWG tunnels: an AmneziaWG
// endpoint (jc junk, s1/s2 handshake junk, s4 transport junk, h1..h4 magic
// headers) running THROUGH another AmneziaWG endpoint via `detour`, fully
// in-process on loopback. The outer pair carries its own (different) AWG
// parameters, so the stand is literally "AWG inside AWG".
//
//	socks client
//	   │ tcp/udp to 10.91.0.1:echo
//	client box: awg-inner ──detour──▶ wg-outer ──real UDP socket──▶ 127.0.0.1
//	                                                                    │
//	server box: wg-outer-server (listen) ──hairpin──▶ awg-inner-server (listen)
//	                                                       │ hairpin
//	                                                       ▼
//	                                                  echo server
//
// Two MTU regimes are exercised:
//   - fits: outer MTU is large enough that an inner AWG datagram (inner MTU +
//     WG overhead + s4 junk) passes the detour netstack as a single packet;
//   - fragments: outer MTU == inner MTU, so every full-size inner datagram
//     must be IP-fragmented by the detour gVisor stack and reassembled on the
//     far side — the worst-case nested configuration users actually run
//     (same MTU everywhere), in both directions.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/netip"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
)

func wgDetourGenKeyPair(t *testing.T) (privateKeyBase64, publicKeyBase64 string) {
	t.Helper()
	privateKey := make([]byte, 32)
	_, err := rand.Read(privateKey)
	require.NoError(t, err)
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(privateKey), base64.StdEncoding.EncodeToString(publicKey)
}

func TestAWGOverAWGDetour_LX(t *testing.T) {
	t.Run("fits", func(t *testing.T) {
		testAWGOverAWGDetour(t, 1420, 1280, 13080)
	})
	t.Run("fragments", func(t *testing.T) {
		testAWGOverAWGDetour(t, 1280, 1280, 13090)
	})
}

func testAWGOverAWGDetour(t *testing.T, outerMTU uint32, innerMTU uint32, basePort uint16) {
	socksPort := basePort
	echoPort := basePort + 1
	outerUDPPort := basePort + 2
	innerUDPPort := basePort + 3

	clientOuterPrivate, clientOuterPublic := wgDetourGenKeyPair(t)
	serverOuterPrivate, serverOuterPublic := wgDetourGenKeyPair(t)
	clientInnerPrivate, clientInnerPublic := wgDetourGenKeyPair(t)
	serverInnerPrivate, serverInnerPublic := wgDetourGenKeyPair(t)

	innerAWGOptions := option.AmneziaWGOptions{
		Jc:   2,
		Jmin: 8,
		Jmax: 16,
		S1:   15,
		S2:   17,
		S4:   60,
		H1:   option.MagicHeader("123456701"),
		H2:   option.MagicHeader("123456702"),
		H3:   option.MagicHeader("123456703"),
		H4:   option.MagicHeader("123456704"),
	}
	outerAWGOptions := option.AmneziaWGOptions{
		Jc:   1,
		Jmin: 8,
		Jmax: 16,
		S1:   11,
		S2:   13,
		H1:   option.MagicHeader("223456701"),
		H2:   option.MagicHeader("223456702"),
		H3:   option.MagicHeader("223456703"),
		H4:   option.MagicHeader("223456704"),
	}

	startInstance(t, option.Options{
		Endpoints: []option.Endpoint{
			{
				Type: C.TypeWireGuard,
				Tag:  "wg-outer-server",
				Options: &option.WireGuardEndpointOptions{
					MTU:              outerMTU,
					Address:          badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.1/24")},
					PrivateKey:       serverOuterPrivate,
					ListenPort:       outerUDPPort,
					AmneziaWGOptions: outerAWGOptions,
					Peers: []option.WireGuardPeer{
						{
							PublicKey:  clientOuterPublic,
							AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.2/32")},
						},
					},
				},
			},
			{
				Type: C.TypeWireGuard,
				Tag:  "awg-inner-server",
				Options: &option.WireGuardEndpointOptions{
					MTU:              innerMTU,
					Address:          badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.1/24")},
					PrivateKey:       serverInnerPrivate,
					ListenPort:       innerUDPPort,
					AmneziaWGOptions: innerAWGOptions,
					Peers: []option.WireGuardPeer{
						{
							PublicKey:  clientInnerPublic,
							AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.2/32")},
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeDirect,
				Tag:  "hairpin-out",
				Options: &option.DirectOutboundOptions{
					OverrideAddress: "127.0.0.1",
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"wg-outer-server", "awg-inner-server"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{
								Outbound: "hairpin-out",
							},
						},
					},
				},
			},
		},
	})

	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: socksPort,
					},
				},
			},
		},
		Endpoints: []option.Endpoint{
			{
				Type: C.TypeWireGuard,
				Tag:  "wg-outer",
				Options: &option.WireGuardEndpointOptions{
					MTU:              outerMTU,
					Address:          badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.2/24")},
					PrivateKey:       clientOuterPrivate,
					AmneziaWGOptions: outerAWGOptions,
					Peers: []option.WireGuardPeer{
						{
							Address:    "127.0.0.1",
							Port:       outerUDPPort,
							PublicKey:  serverOuterPublic,
							AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.1/32")},
						},
					},
				},
			},
			{
				Type: C.TypeWireGuard,
				Tag:  "awg-inner",
				Options: &option.WireGuardEndpointOptions{
					MTU:              innerMTU,
					Address:          badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.2/24")},
					PrivateKey:       clientInnerPrivate,
					AmneziaWGOptions: innerAWGOptions,
					Peers: []option.WireGuardPeer{
						{
							Address:    "10.90.0.1",
							Port:       innerUDPPort,
							PublicKey:  serverInnerPublic,
							AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.1/32")},
						},
					},
					DialerOptions: option.DialerOptions{
						Detour: "wg-outer",
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{
								Outbound: "awg-inner",
							},
						},
					},
				},
			},
		},
	})

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", socksPort), socks.Version5, "", "")
	dialTCP := func() (net.Conn, error) {
		return dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("10.91.0.1", echoPort))
	}
	dialUDP := func() (net.PacketConn, error) {
		conn, err := dialer.DialContext(context.Background(), "udp", M.ParseSocksaddrHostPort("10.91.0.1", echoPort))
		if err != nil {
			return nil, err
		}
		return bufio.NewUnbindPacketConn(conn), nil
	}
	require.NoError(t, testPingPongWithConn(t, echoPort, dialTCP))
	require.NoError(t, testPingPongWithPacketConn(t, echoPort, dialUDP))
	require.NoError(t, testLargeDataWithConn(t, echoPort, dialTCP))
	require.NoError(t, testLargeDataWithPacketConn(t, echoPort, dialUDP))
}
