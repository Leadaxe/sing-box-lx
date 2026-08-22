//go:build with_lx_chain

// lx: SPECS/TASKS/073-CHAIN_OUTBOUND — приёмочный стенд цепочки на живых
// протоколах: три shadowsocks-сервера в одном box'е как хопы, mixed-inbound как
// вход трафика, локальный HTTP-сервер как цель. Проверяет, что вложение
// «in → mid → exit» реально несёт трафик, что прозрачный direct в середине
// укорачивает путь на лету и что детур звеньев ходит через внутренние теги.
//
// Живёт здесь, а не в test/, по той же причине, что lx-test/zombie: модуль test/
// резолвит форк-сабмодули через прокси и не собирается.
package chain

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

const (
	ssMethod   = "2022-blake3-aes-128-gcm"
	ssPassword = "8JCsPssfgS8tiRwiMlhARg=="
)

func freePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return uint16(port)
}

func listenOn(port uint16) option.ListenOptions {
	addr := badoption.Addr(netip.MustParseAddr("127.0.0.1"))
	return option.ListenOptions{Listen: &addr, ListenPort: port}
}

func ssServer(tag string, port uint16) option.Inbound {
	return option.Inbound{Type: C.TypeShadowsocks, Tag: tag, Options: &option.ShadowsocksInboundOptions{
		ListenOptions: listenOn(port), Method: ssMethod, Password: ssPassword,
	}}
}

func ssClient(tag string, port uint16) option.Outbound {
	return option.Outbound{Type: C.TypeShadowsocks, Tag: tag, Options: &option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port},
		Method:        ssMethod, Password: ssPassword,
	}}
}

func TestChainEndToEndShadowsocksHops(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through-the-chain")
	}))
	defer target.Close()

	inPort, midPort, exitPort, mixedPort := freePort(t), freePort(t), freePort(t), freePort(t)
	ctx := include.Context(context.Background())
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{Level: "warning"},
			Inbounds: []option.Inbound{
				ssServer("srv-in", inPort),
				ssServer("srv-mid", midPort),
				ssServer("srv-exit", exitPort),
				{Type: C.TypeMixed, Tag: "mixed", Options: &option.HTTPMixedInboundOptions{ListenOptions: listenOn(mixedPort)}},
			},
			Outbounds: []option.Outbound{
				ssClient("in", inPort),
				ssClient("mid", midPort),
				ssClient("exit", exitPort),
				{Type: C.TypeDirect, Tag: "direct"},
				{Type: C.TypeSelector, Tag: "sel-mid", Options: &option.SelectorOutboundOptions{Outbounds: []string{"mid", "direct"}, InterruptExistConnections: true}},
				{Type: C.TypeChain, Tag: "virt", Options: &option.ChainOutboundOptions{Outbounds: []string{"in", "sel-mid", "exit"}}},
			},
			Route: &option.RouteOptions{
				Final: "virt",
				Rules: []option.Rule{{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						// Серверные половины хопов выпускают свой трафик напрямую —
						// иначе ss-сервер отправил бы следующий хоп снова в цепочку.
						RawDefaultRule: option.RawDefaultRule{Inbound: []string{"srv-in", "srv-mid", "srv-exit"}},
						RuleAction:     option.RuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.RouteActionOptions{Outbound: "direct"}},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	defer instance.Close()

	proxyURL, _ := url.Parse("socks5://127.0.0.1:" + strconv.Itoa(int(mixedPort)))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 10 * time.Second}
	get := func() string {
		resp, err := client.Get(target.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return string(body)
	}

	chainOutbound, loaded := instance.Outbound().Outbound("virt")
	require.True(t, loaded)
	pathProvider := chainOutbound.(adapter.ChainPathProvider)
	statusProvider := chainOutbound.(adapter.ChainStatusProvider)

	// Прогрев: узел на позиции 2 и селектор на позиции 1 детерминированы → два звена.
	require.EqualValues(t, 2, statusProvider.ChainStatus().LiveClones)
	require.Equal(t, "in,mid,exit", strings.Join(pathProvider.ChainPath(), ","))

	require.Equal(t, "through-the-chain", get())
	status := statusProvider.ChainStatus()
	require.EqualValues(t, 1, status.Dials)
	require.EqualValues(t, 0, status.Errors)

	// Хопы адресуемы по тегу (URLTest по префиксу), но не в списке outbound'ов.
	_, hopLoaded := instance.Outbound().Outbound("virt#1")
	require.True(t, hopLoaded)
	for _, ob := range instance.Outbound().Outbounds() {
		require.NotEqual(t, "virt#1", ob.Tag())
	}

	// direct в середине: путь укорачивается без перезапуска.
	selector, _ := instance.Outbound().Outbound("sel-mid")
	require.True(t, selector.(*group.Selector).SelectOutbound("direct"))
	require.Equal(t, "in,exit", strings.Join(pathProvider.ChainPath(), ","))
	require.Equal(t, "through-the-chain", get())
	require.True(t, statusProvider.ChainStatus().Positions[1].Transparent)

	// и обратно
	require.True(t, selector.(*group.Selector).SelectOutbound("mid"))
	require.Equal(t, "in,mid,exit", strings.Join(pathProvider.ChainPath(), ","))
	require.Equal(t, "through-the-chain", get())
}

// TestChainCheckRejectsWithoutHops — минимум две позиции, ошибка на чтении.
func TestChainRejectsSinglePosition(t *testing.T) {
	ctx := include.Context(context.Background())
	_, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log:       &option.LogOptions{Level: "warning"},
			Outbounds: []option.Outbound{{Type: C.TypeDirect, Tag: "direct"}, {Type: C.TypeChain, Tag: "virt", Options: &option.ChainOutboundOptions{Outbounds: []string{"direct"}}}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 2 outbounds")
}
