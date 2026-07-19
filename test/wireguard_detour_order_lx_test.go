//go:build with_gvisor && with_wireguard

// lx: SPEC 029 — regression for the detour start-order bug. A WireGuard
// endpoint with `detour: X` used to resolve its detour eagerly inside its
// constructor (a UDPListener type-assertion that walked the dialer's Upstream()
// chain and fired DetourDialer's sync.Once). Because endpoints are constructed
// in config-array order and only registered at the end of their own Create(),
// an endpoint whose detour pointed at a provider declared LATER in the array
// resolved it before the provider existed — caching "outbound detour not found"
// forever, so the tunnel never carried a byte.
//
// This stand declares the consumer (wg-inner, detour → wg-outer) BEFORE the
// provider (wg-outer) in the endpoints array — the exact ordering that broke.
// With the fix the detour is resolved in Start, after the dependency topo-sort
// has brought wg-outer up, so array order no longer matters and traffic flows.
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

func wgOrderGenKeyPair(t *testing.T) (privateKeyBase64, publicKeyBase64 string) {
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

func TestWGDetourStartOrder_LX(t *testing.T) {
	const (
		socksPort    = 13100
		echoPort     = 13101
		outerUDPPort = 13102
		innerUDPPort = 13103
	)

	clientOuterPrivate, clientOuterPublic := wgOrderGenKeyPair(t)
	serverOuterPrivate, serverOuterPublic := wgOrderGenKeyPair(t)
	clientInnerPrivate, clientInnerPublic := wgOrderGenKeyPair(t)
	serverInnerPrivate, serverInnerPublic := wgOrderGenKeyPair(t)

	// Server box: outer WG terminator hairpins into the inner WG terminator,
	// which routes decrypted traffic straight to the loopback echo server.
	startInstance(t, option.Options{
		Endpoints: []option.Endpoint{
			{
				Type: C.TypeWireGuard,
				Tag:  "wg-outer-server",
				Options: &option.WireGuardEndpointOptions{
					MTU:        1420,
					Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.1/24")},
					PrivateKey: serverOuterPrivate,
					ListenPort: outerUDPPort,
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
				Tag:  "wg-inner-server",
				Options: &option.WireGuardEndpointOptions{
					MTU:        1280,
					Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.1/24")},
					PrivateKey: serverInnerPrivate,
					ListenPort: innerUDPPort,
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
			{Type: C.TypeDirect},
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
							Inbound: []string{"wg-outer-server", "wg-inner-server"},
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

	// Client box: the CONSUMER (wg-inner, detour → wg-outer) is declared FIRST,
	// the PROVIDER (wg-outer) SECOND. This is the ordering that used to cache
	// "detour not found" forever.
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
				Tag:  "wg-inner",
				Options: &option.WireGuardEndpointOptions{
					MTU:        1280,
					Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.91.0.2/24")},
					PrivateKey: clientInnerPrivate,
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
			{
				Type: C.TypeWireGuard,
				Tag:  "wg-outer",
				Options: &option.WireGuardEndpointOptions{
					MTU:        1420,
					Address:    badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.90.0.2/24")},
					PrivateKey: clientOuterPrivate,
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
								Outbound: "wg-inner",
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
	// Before the fix: wg-inner cached "detour not found: wg-outer" at construction,
	// so no handshake ever left the box and these time out. After the fix: the
	// detour resolves in Start behind the topo-sort and traffic flows.
	require.NoError(t, testPingPongWithConn(t, echoPort, dialTCP))
	require.NoError(t, testPingPongWithPacketConn(t, echoPort, dialUDP))
	require.NoError(t, testLargeDataWithConn(t, echoPort, dialTCP))
}
