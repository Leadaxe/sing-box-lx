//go:build with_lx_chain && with_wireguard

// lx: SPEC 165 — критерий приёмки 1 в миниатюре, на живом WireGuard: группа
// на позиции 1 цепочки из WG-endpoint'а, у которого собственный detour ведёт
// в мёртвый узел. После перезаписи endpoint один и ходит только путём
// цепочки — через shadowsocks на позиции 0 к WG-серверу в том же box'е.
package chain

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
)

func wgKeyPair(t *testing.T) (string, string) {
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

func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

func TestChainWireGuardOnPositionТ1(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through-the-wg-chain")
	}))
	defer target.Close()
	targetPort, _ := strconv.Atoi(target.URL[len("http://127.0.0.1:"):])

	serverPrivate, serverPublic := wgKeyPair(t)
	clientPrivate, clientPublic := wgKeyPair(t)
	inPort, wgPort, mixedPort, deadPort := freePort(t), freeUDPPort(t), freePort(t), freePort(t)
	options := option.Options{
		Log: &option.LogOptions{Level: "warning"},
		Inbounds: []option.Inbound{
			ssServer("srv-in", inPort),
			{Type: C.TypeMixed, Tag: "mixed", Options: &option.HTTPMixedInboundOptions{ListenOptions: listenOn(mixedPort)}},
		},
		Endpoints: []option.Endpoint{
			{Type: C.TypeWireGuard, Tag: "wg-srv", Options: &option.WireGuardEndpointOptions{
				Address: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.77.0.1/32")}, PrivateKey: serverPrivate, ListenPort: wgPort, MTU: 1300,
				Peers: []option.WireGuardPeer{{PublicKey: clientPublic, AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.77.0.2/32")}}},
			}},
			{Type: C.TypeWireGuard, Tag: "wg-node", Options: &option.WireGuardEndpointOptions{
				Address: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.77.0.2/32")}, PrivateKey: clientPrivate, MTU: 1300,
				Peers: []option.WireGuardPeer{{
					Address: "127.0.0.1", Port: wgPort, PublicKey: serverPublic,
					AllowedIPs: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("10.77.0.1/32")},
				}},
				// Its own detour is dead: only the chain's path works.
				DialerOptions: option.DialerOptions{Detour: "o-dead"},
			}},
		},
		Outbounds: []option.Outbound{
			ssClient("o-in", inPort),
			ssClient("o-dead", deadPort),
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeURLTest, Tag: "o-wg", Options: &option.URLTestOutboundOptions{Outbounds: []string{"wg-node"}}},
			{Type: C.TypeChain, Tag: "c-wg", Options: &option.ChainOutboundOptions{Outbounds: []string{"o-in", "o-wg"}}},
		},
		Route: &option.RouteOptions{
			Final: "c-wg",
			Rules: []option.Rule{
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"srv-in"}},
					RuleAction:     option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: "direct"}},
				}},
				// The WG server lets the tunnel reach the local target: the
				// netstack drops loopback destinations, so the client asks for
				// 10.77.0.1 and the route rewrites it.
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"wg-srv"}},
					RuleAction: option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{
						Outbound: "direct", RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{OverrideAddress: "127.0.0.1", OverridePort: uint16(targetPort)},
					}},
				}},
			},
		},
	}
	instance, err := box.New(box.Options{Context: include.Context(context.Background()), Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	defer instance.Close()

	node, loaded := instance.Outbound().Outbound("wg-node")
	require.True(t, loaded)
	require.Equal(t, []string{"c-wg#0"}, node.Dependencies(), "П2: the node's own detour is taken over")

	proxyURL, _ := url.Parse("socks5://127.0.0.1:" + strconv.Itoa(int(mixedPort)))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 15 * time.Second}
	get := func(host string) string {
		resp, err := client.Get("http://" + host + "/")
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(body)
	}
	require.Equal(t, "through-the-wg-chain", get("10.77.0.1:80"))
	// И3: the node the group checks is the one that carries the traffic, so
	// a direct dial of the node (as the group's probe does) works too.
	conn, err := node.DialContext(context.Background(), "tcp", parseSocks("10.77.0.1:80"))
	require.NoError(t, err)
	conn.Close()
}

func parseSocks(address string) M.Socksaddr {
	return M.ParseSocksaddr(address)
}
