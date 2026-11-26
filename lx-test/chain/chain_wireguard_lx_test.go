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
	"github.com/sagernet/sing-box/adapter"
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

// TestChainGroupMeasuresChainPathКритерий1 — критерий приёмки 1: urltest на
// позиции 1 из двух WG-узлов. Второй узел жив своим путём (detour direct), но
// мёртв путём цепочки: shadowsocks на позиции 0 не пропускает UDP к его
// серверу. Группа меряет путь цепочки (И3) — второй узел не проходит, выбран
// первый. При копиях-звеньях группа мерила бы оригиналы и считала оба живыми.
func TestChainGroupMeasuresChainPathКритерий1(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	targetPort, _ := strconv.Atoi(target.URL[len("http://127.0.0.1:"):])

	server1Private, server1Public := wgKeyPair(t)
	server2Private, server2Public := wgKeyPair(t)
	client1Private, client1Public := wgKeyPair(t)
	client2Private, client2Public := wgKeyPair(t)
	inPort, wg1Port, wg2Port := freePort(t), freeUDPPort(t), freeUDPPort(t)
	prefix := func(text string) badoption.Listable[netip.Prefix] {
		return badoption.Listable[netip.Prefix]{netip.MustParsePrefix(text)}
	}
	server := func(tag, private, clientPublic string, port uint16, address, peer string) option.Endpoint {
		return option.Endpoint{Type: C.TypeWireGuard, Tag: tag, Options: &option.WireGuardEndpointOptions{
			Address: prefix(address), PrivateKey: private, ListenPort: port, MTU: 1300,
			Peers: []option.WireGuardPeer{{PublicKey: clientPublic, AllowedIPs: prefix(peer)}},
		}}
	}
	node := func(tag, private, serverPublic string, port uint16, address string) option.Endpoint {
		return option.Endpoint{Type: C.TypeWireGuard, Tag: tag, Options: &option.WireGuardEndpointOptions{
			Address: prefix(address), PrivateKey: private, MTU: 1300,
			Peers:         []option.WireGuardPeer{{Address: "127.0.0.1", Port: port, PublicKey: serverPublic, AllowedIPs: prefix("10.77.0.1/32")}},
			DialerOptions: option.DialerOptions{Detour: "direct"},
		}}
	}
	toTarget := option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{
		Outbound: "direct", RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{OverrideAddress: "127.0.0.1", OverridePort: uint16(targetPort)},
	}}
	options := option.Options{
		Log:      &option.LogOptions{Level: "error"},
		Inbounds: []option.Inbound{ssServer("srv-in", inPort)},
		Endpoints: []option.Endpoint{
			server("wg-srv1", server1Private, client1Public, wg1Port, "10.77.0.1/32", "10.77.0.2/32"),
			server("wg-srv2", server2Private, client2Public, wg2Port, "10.77.0.1/32", "10.77.0.3/32"),
			node("NL-1", client1Private, server1Public, wg1Port, "10.77.0.2/32"),
			node("NL-2", client2Private, server2Public, wg2Port, "10.77.0.3/32"),
		},
		Outbounds: []option.Outbound{
			ssClient("o-in", inPort),
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeURLTest, Tag: "o-wg", Options: &option.URLTestOutboundOptions{
				Outbounds: []string{"NL-2", "NL-1"}, URL: "http://10.77.0.1/", Interval: badoption.Duration(10 * time.Minute),
			}},
			// The entry is a group, as o-direct in LxBox#233.
			{Type: C.TypeSelector, Tag: "o-entry", Options: &option.SelectorOutboundOptions{Outbounds: []string{"o-in"}}},
			{Type: C.TypeChain, Tag: "c-wg", Options: &option.ChainOutboundOptions{Outbounds: []string{"o-entry", "o-wg"}}},
		},
		Route: &option.RouteOptions{
			Final: "c-wg",
			Rules: []option.Rule{
				// The path of the chain does not reach the second server.
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"srv-in"}, Port: badoption.Listable[uint16]{wg2Port}},
					RuleAction:     option.RuleAction{Action: C.RuleActionTypeReject, RejectOptions: option.RejectActionOptions{Method: C.RuleActionRejectMethodDrop}},
				}},
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"srv-in"}},
					RuleAction:     option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: "direct"}},
				}},
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{Inbound: []string{"wg-srv1", "wg-srv2"}},
					RuleAction:     toTarget,
				}},
			},
		},
	}
	instance, err := box.New(box.Options{Context: include.Context(context.Background()), Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	defer instance.Close()

	groupOutbound, loaded := instance.Outbound().Outbound("o-wg")
	require.True(t, loaded)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The group runs its own first check at start; a manual run during it
	// returns at once, so ask until the group answers.
	var result map[string]uint16
	for len(result) == 0 && ctx.Err() == nil {
		result, _ = groupOutbound.(adapter.URLTestGroup).URLTest(ctx)
		if len(result) == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	_, firstAlive := result["NL-1"]
	_, secondAlive := result["NL-2"]
	require.True(t, firstAlive, "NL-1 is alive on the chain's path: %v", result)
	require.False(t, secondAlive, "И3: NL-2 is dead on the chain's path and must not pass the group's check: %v", result)
	require.Equal(t, "NL-1", adapter.GroupNow(groupOutbound.(adapter.OutboundGroup)))
}
