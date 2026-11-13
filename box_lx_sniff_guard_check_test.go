package box_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/anytls"
	"github.com/sagernet/sing-box/protocol/http"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/shadowtls"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/trusttunnel"
	"github.com/sagernet/sing-box/protocol/tuic"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

// sniffGuardCheck runs the path of `sing-box check`: parse, then box.New.
func sniffGuardCheck(t *testing.T, config string) (context.Context, *box.Box, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(service.ExtendContext(include.Context(context.Background())))
	t.Cleanup(cancel)
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(config))
	if err != nil {
		return ctx, nil, err
	}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return ctx, nil, err
	}
	t.Cleanup(func() { instance.Close() })
	return ctx, instance, nil
}

const sniffGuardOutbounds = `[
  {"type": "vless", "tag": "vless", "server": "node.example", "server_port": 443, "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "vless", "tag": "vless-own", "server": "node.example", "server_port": 443, "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "tls": {"enabled": true}},
  {"type": "vmess", "tag": "vmess", "server": "node.example", "server_port": 443, "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "trojan", "tag": "trojan", "server": "node.example", "server_port": 443, "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "anytls", "tag": "anytls", "server": "node.example", "server_port": 443, "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "shadowtls", "tag": "shadowtls", "server": "node.example", "server_port": 443, "version": 3, "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "http", "tag": "https", "server": "node.example", "server_port": 443, "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "http", "tag": "http-plain", "server": "node.example", "server_port": 8080},
  {"type": "hysteria2", "tag": "hysteria2", "server": "node.example", "server_port": 443, "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "tuic", "tag": "tuic", "server": "node.example", "server_port": 443, "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "trusttunnel", "tag": "trusttunnel", "server": "node.example", "server_port": 443, "username": "u", "password": "p", "tls": {"enabled": true, "server_name": "cover.example"}},
  {"type": "socks", "tag": "socks", "server": "127.0.0.1", "server_port": 1080},
  {"type": "selector", "tag": "group", "outbounds": ["vless", "trojan"]}
]`

func sniffGuardConfig(block string, extra string) string {
	return `{"log": {"disabled": true}, "outbounds": ` + sniffGuardOutbounds + `, "lx": {"sniff_guard": ` + block + `}` + extra + `}`
}

const sniffGuardFullBlock = `{
  "nodes": {"sni_mismatch": false, "outbound_type": false},
  "detour_type": {"direct": "clean", "ssh": "clean", "socks": "clean", "wireguard": "suspect", "*": "suspect"},
  "override": false,
  "tls": {"method": "record_fragment", "delay": "300ms", "persist": true},
  "quic": {"method": "decoy_first_byte", "detour_type": {"socks": "never"}, "persist": true}
}`

func methodNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, t.Method(i).Name)
	}
	sort.Strings(names)
	return names
}

// lx: SPEC 060 — a full block loads; every covered node comes out wrapped
// with exactly the method set of its protocol type, the rest unwrapped.
func TestSniffGuardCheckFullBlock(t *testing.T) {
	ctx, _, err := sniffGuardCheck(t, sniffGuardConfig(sniffGuardFullBlock, `, "experimental": {"cache_file": {"enabled": true, "path": "`+t.TempDir()+`/cache.db"}}`))
	require.NoError(t, err)
	manager := service.FromContext[adapter.OutboundManager](ctx)
	require.NotNil(t, manager)
	covered := map[string]reflect.Type{
		"vless":       reflect.TypeOf((*vless.Outbound)(nil)),
		"vless-own":   reflect.TypeOf((*vless.Outbound)(nil)),
		"vmess":       reflect.TypeOf((*vmess.Outbound)(nil)),
		"trojan":      reflect.TypeOf((*trojan.Outbound)(nil)),
		"anytls":      reflect.TypeOf((*anytls.Outbound)(nil)),
		"shadowtls":   reflect.TypeOf((*shadowtls.Outbound)(nil)),
		"https":       reflect.TypeOf((*http.Outbound)(nil)),
		"hysteria2":   reflect.TypeOf((*hysteria2.Outbound)(nil)),
		"tuic":        reflect.TypeOf((*tuic.Outbound)(nil)),
		"trusttunnel": reflect.TypeOf((*trusttunnel.Outbound)(nil)),
	}
	for tag, protocolType := range covered {
		outbound, loaded := manager.Outbound(tag)
		require.True(t, loaded, tag)
		require.Contains(t, fmt.Sprintf("%T", outbound), "sniffGuardOutbound", tag)
		require.Equal(t, methodNames(protocolType), methodNames(reflect.TypeOf(outbound)), tag)
	}
	for _, tag := range []string{"http-plain", "socks", "group"} {
		outbound, loaded := manager.Outbound(tag)
		require.True(t, loaded, tag)
		require.NotContains(t, fmt.Sprintf("%T", outbound), "sniffGuardOutbound", tag)
	}
}

// The filters: sni_mismatch leaves the node with its own name unwrapped,
// outbound_type everything not listed.
func TestSniffGuardCheckNodeFilters(t *testing.T) {
	ctx, _, err := sniffGuardCheck(t, sniffGuardConfig(`{"nodes": {"sni_mismatch": true, "outbound_type": ["vless", "naive"]}, "tls": {"method": "delay"}}`, ""))
	require.NoError(t, err)
	manager := service.FromContext[adapter.OutboundManager](ctx)
	wrapped := func(tag string) bool {
		outbound, loaded := manager.Outbound(tag)
		require.True(t, loaded, tag)
		return strings.Contains(fmt.Sprintf("%T", outbound), "sniffGuardOutbound")
	}
	require.True(t, wrapped("vless"))
	require.False(t, wrapped("vless-own"), "sni matches server")
	require.False(t, wrapped("trojan"), "not in outbound_type")
}

// Without a method the block is off: nothing is wrapped.
func TestSniffGuardCheckOff(t *testing.T) {
	ctx, _, err := sniffGuardCheck(t, sniffGuardConfig(`{"detour_type": {"socks": "never"}, "tls": {"persist": true}}`, ""))
	require.NoError(t, err)
	outbound, _ := service.FromContext[adapter.OutboundManager](ctx).Outbound("vless")
	require.NotContains(t, fmt.Sprintf("%T", outbound), "sniffGuardOutbound")
}

// Every load error is caught on the check path and names its key.
func TestSniffGuardCheckErrors(t *testing.T) {
	for _, testCase := range []struct {
		block string
		extra string
		want  string
	}{
		{`{"detour_type": {"nope": "clean"}, "tls": {"method": "record_fragment"}}`, "", `lx.sniff_guard.detour_type: unknown outbound type "nope"`},
		{`{"tls": {"method": "record_fragment", "detour_type": {"nope": "clean"}}}`, "", `lx.sniff_guard.tls.detour_type: unknown outbound type "nope"`},
		{`{"quic": {"method": "decoy_first_byte", "detour_type": {"nope": "never"}}}`, "", `lx.sniff_guard.quic.detour_type: unknown outbound type "nope"`},
		{`{"nodes": {"outbound_type": ["vless", "nope"]}, "tls": {"method": "record_fragment"}}`, "", `lx.sniff_guard.nodes.outbound_type: unknown outbound type "nope"`},
		{`{"nodes": {"outbound_type": []}, "tls": {"method": "record_fragment"}}`, "", `lx.sniff_guard.nodes.outbound_type`},
		{`{"nodes": {"outbound_type": true}, "tls": {"method": "record_fragment"}}`, "", `lx.sniff_guard.nodes.outbound_type`},
		{`{"tls": {"method": "record_fragment", "persist": true}}`, "", `lx.sniff_guard.tls.persist`},
		{`{"quic": {"method": "decoy_first_byte", "persist": true}}`, `, "experimental": {"cache_file": {"enabled": false}}`, `lx.sniff_guard.quic.persist`},
	} {
		_, _, err := sniffGuardCheck(t, sniffGuardConfig(testCase.block, testCase.extra))
		require.Error(t, err, testCase.block)
		require.Contains(t, err.Error(), testCase.want, testCase.block)
	}
}
