package box

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/masque"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/wireguard"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 120 §6.2 — the detour pre-pass on option.Options, no box started.
// Configs are decoded through the real option registries so the structs are
// the same pointers the constructors would read.

func mtuTestContext() context.Context {
	outboundRegistry := outbound.NewRegistry()
	hysteria2.RegisterOutbound(outboundRegistry)
	masque.RegisterOutbound(outboundRegistry)
	vless.RegisterOutbound(outboundRegistry)
	direct.RegisterOutbound(outboundRegistry)
	group.RegisterSelector(outboundRegistry)
	group.RegisterURLTest(outboundRegistry)
	endpointRegistry := endpoint.NewRegistry()
	wireguard.RegisterEndpoint(endpointRegistry)
	ctx := service.ContextWithDefaultRegistry(context.Background())
	ctx = service.ContextWith[option.OutboundOptionsRegistry](ctx, outboundRegistry)
	ctx = service.ContextWith[adapter.OutboundRegistry](ctx, outboundRegistry)
	ctx = service.ContextWith[option.EndpointOptionsRegistry](ctx, endpointRegistry)
	ctx = service.ContextWith[adapter.EndpointRegistry](ctx, endpointRegistry)
	return ctx
}

// Issue #37: hysteria2 over masque (WARP) through detour, IPv6 hysteria2
// server. Keys are placeholders.
const issue37Config = `{
  "outbounds": [
    {
      "type": "hysteria2",
      "tag": "hy2",
      "server": "{{server}}",
      "server_port": 443,
      "password": "x",
      "tls": {"enabled": true, "server_name": "hy2.example.com"},
      "detour": "warp"{{hy2}}
    },
    {
      "type": "masque",
      "tag": "warp",
      "server": "2606:4700:104::2",
      "server_port": 443,
      "private_key": "AA==",
      "public_key": "AA==",
      "ip": "172.16.0.2",
      "mtu": 1280
    },
    {"type": "direct", "tag": "direct"}
  ]{{root}}
}`

// issue37 fills the template: the hysteria2 server, extra hysteria2 keys
// (", key: value") and extra root keys (", lx: {...}").
func issue37(server, hy2Extra, rootExtra string) string {
	replacer := strings.NewReplacer("{{server}}", server, "{{hy2}}", hy2Extra, "{{root}}", rootExtra)
	return replacer.Replace(issue37Config)
}

type mtuTestLog struct {
	log.Logger
	info []string
	warn []string
}

func (l *mtuTestLog) Info(args ...any)  { l.info = append(l.info, F.ToString(args...)) }
func (l *mtuTestLog) Warn(args ...any)  { l.warn = append(l.warn, F.ToString(args...)) }
func (l *mtuTestLog) Error(args ...any) { l.warn = append(l.warn, F.ToString(args...)) }

func decodeMTUConfig(t *testing.T, content string) option.Options {
	t.Helper()
	var options option.Options
	if err := json.UnmarshalContext(mtuTestContext(), []byte(content), &options); err != nil {
		t.Fatalf("decode: %v\n%s", err, content)
	}
	return options
}

func applyMTU(t *testing.T, content string) (option.Options, *mtuTestLog, error) {
	t.Helper()
	options := decodeMTUConfig(t, content)
	logger := &mtuTestLog{Logger: log.NewNOPFactory().Logger()}
	_, err := applyLXOptions(mtuTestContext(), &options, logger)
	return options, logger, err
}

func outboundOptions[T any](t *testing.T, options option.Options, tag string) *T {
	t.Helper()
	for _, outbound := range options.Outbounds {
		if outbound.Tag == tag {
			typed, ok := outbound.Options.(*T)
			if !ok {
				t.Fatalf("%s: options are %T", tag, outbound.Options)
			}
			return typed
		}
	}
	t.Fatalf("outbound %s not found", tag)
	return nil
}

func endpointOptions[T any](t *testing.T, options option.Options, tag string) *T {
	t.Helper()
	for _, endpoint := range options.Endpoints {
		if endpoint.Tag == tag {
			typed, ok := endpoint.Options.(*T)
			if !ok {
				t.Fatalf("%s: options are %T", tag, endpoint.Options)
			}
			return typed
		}
	}
	t.Fatalf("endpoint %s not found", tag)
	return nil
}

func TestMTUAlignIssue37(t *testing.T) {
	t.Run("ipv6 server → 1232, pmtud off", func(t *testing.T) {
		options, logger, err := applyMTU(t, issue37("2606:4700:104::1", "", ""))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1232 || !hy2.DisablePathMTUDiscovery {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
		if len(logger.info) != 1 || !strings.HasPrefix(logger.info[0], "mtu_align: hy2 initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)") {
			t.Fatalf("info %v", logger.info)
		}
		if len(logger.warn) != 0 {
			t.Fatalf("warn %v", logger.warn)
		}
		// masque itself has no detour and stays as configured
		if warp := outboundOptions[option.MASQUEOutboundOptions](t, options, "warp"); warp.MTU != 1280 {
			t.Fatalf("%d", warp.MTU)
		}
	})
	t.Run("ipv4 server → 1252", func(t *testing.T) {
		options, _, err := applyMTU(t, issue37("1.2.3.4", "", ""))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1252 || !hy2.DisablePathMTUDiscovery {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
	})
	t.Run("explicit 1300 in clamp → 1232", func(t *testing.T) {
		options, logger, err := applyMTU(t, issue37("2606:4700:104::1", `, "initial_packet_size": 1300`, `, "lx": {"mtu_align": "clamp"}`))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1232 || !hy2.DisablePathMTUDiscovery {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
		if len(logger.info) != 1 || !strings.Contains(logger.info[0], "1300 → 1232 clamped") {
			t.Fatalf("info %v", logger.info)
		}
	})
	t.Run("explicit 1300 in fill → kept + warning", func(t *testing.T) {
		options, logger, err := applyMTU(t, issue37("2606:4700:104::1", `, "initial_packet_size": 1300`, `, "lx": {"mtu_align": {"mode": "fill"}}`))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1300 || hy2.DisablePathMTUDiscovery {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
		if len(logger.info) != 0 || len(logger.warn) != 1 || !strings.Contains(logger.warn[0], "mtu_align: hy2 initial_packet_size 1300 does not fit") {
			t.Fatalf("info %v warn %v", logger.info, logger.warn)
		}
	})
	t.Run("off changes nothing and logs nothing", func(t *testing.T) {
		options, logger, err := applyMTU(t, issue37("2606:4700:104::1", "", `, "lx": {"mtu_align": "off"}`))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 0 || hy2.DisablePathMTUDiscovery || len(logger.info) != 0 || len(logger.warn) != 0 {
			t.Fatalf("%+v %v %v", hy2.QUICOptions, logger.info, logger.warn)
		}
	})
	t.Run("except keeps the node", func(t *testing.T) {
		options, logger, err := applyMTU(t, issue37("2606:4700:104::1", "", `, "lx": {"mtu_align": {"except": ["hy2"]}}`))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 0 || hy2.DisablePathMTUDiscovery || len(logger.info) != 0 {
			t.Fatalf("%+v %v", hy2.QUICOptions, logger.info)
		}
	})
	t.Run("unknown except tag fails start", func(t *testing.T) {
		_, _, err := applyMTU(t, issue37("2606:4700:104::1", "", `, "lx": {"mtu_align": {"except": ["nope"]}}`))
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("%v", err)
		}
	})
}

const groupConfig = `{
  "outbounds": [
    {"type": "hysteria2", "tag": "hy2", "server": "2606:4700:104::1", "server_port": 443, "password": "x",
     "tls": {"enabled": true, "server_name": "hy2.example.com"}, "detour": "{{detour}}"},
    {"type": "selector", "tag": "sel", "outbounds": ["direct", "warp", "vless-via-wg"]},
    {"type": "urltest", "tag": "auto", "outbounds": ["sel"]},
    {"type": "vless", "tag": "vless-via-wg", "server": "1.2.3.4", "server_port": 443,
     "uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "detour": "wg"},
    {"type": "masque", "tag": "warp", "server": "162.159.192.1", "server_port": 443,
     "private_key": "AA==", "public_key": "AA==", "ip": "172.16.0.2", "mtu": 1340},
    {"type": "direct", "tag": "direct"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "wg", "address": ["10.0.0.2/32"], "private_key": "AA==",
     "peers": [{"address": "2001:db8::1", "port": 51820, "public_key": "AA==", "allowed_ips": ["0.0.0.0/0"]}]{{wg}}}
  ]
}`

func groupGraph(detour, wgExtra string) string {
	return strings.NewReplacer("{{detour}}", detour, "{{wg}}", wgExtra).Replace(groupConfig)
}

func TestMTUAlignGraph(t *testing.T) {
	t.Run("selector under detour takes the minimum", func(t *testing.T) {
		// members: direct ∞, warp 1340, vless → wg 1408: min 1340 → hy2 1340 − 48 = 1292
		options, logger, err := applyMTU(t, groupGraph("sel", ""))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1292 || !hy2.DisablePathMTUDiscovery {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
		if len(logger.info) != 1 || !strings.Contains(logger.info[0], "warp[masque] mtu 1340 via sel") {
			t.Fatalf("%v", logger.info)
		}
	})
	t.Run("nested group and a narrower wg", func(t *testing.T) {
		// wg 1300 becomes the minimum through vless-via-wg: 1300 − 48 = 1252
		options, logger, err := applyMTU(t, groupGraph("auto", `, "mtu": 1300`))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1252 {
			t.Fatalf("%+v", hy2.QUICOptions)
		}
		if len(logger.info) != 1 || !strings.Contains(logger.info[0], "wg[wireguard] mtu 1300 via vless-via-wg via sel via auto") {
			t.Fatalf("%v", logger.info)
		}
	})
	t.Run("transitive detour", func(t *testing.T) {
		options, _, err := applyMTU(t, groupGraph("vless-via-wg", ""))
		if err != nil {
			t.Fatal(err)
		}
		hy2 := outboundOptions[option.Hysteria2OutboundOptions](t, options, "hy2")
		if hy2.InitialPacketSize != 1360 {
			t.Fatalf("1408 − 48, got %+v", hy2.QUICOptions)
		}
	})
}

// A WireGuard endpoint over masque: the WG overhead by the peer's family
// (80 for an IPv6 peer) is subtracted from the masque mtu.
func TestMTUAlignWireGuardOverMASQUE(t *testing.T) {
	const content = `{
  "outbounds": [
    {"type": "masque", "tag": "warp", "server": "162.159.192.1", "server_port": 443,
     "private_key": "AA==", "public_key": "AA==", "ip": "172.16.0.2"},
    {"type": "direct", "tag": "direct"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "wg-v6", "address": ["10.0.0.2/32"], "private_key": "AA==", "detour": "warp",
     "peers": [{"address": "2001:db8::1", "port": 51820, "public_key": "AA==", "allowed_ips": ["0.0.0.0/0"]}]},
    {"type": "wireguard", "tag": "wg-v4", "address": ["10.0.0.3/32"], "private_key": "AA==", "detour": "warp", "mtu": 1420,
     "peers": [{"address": "1.2.3.4", "port": 51820, "public_key": "AA==", "allowed_ips": ["0.0.0.0/0"]}]}
  ]
}`
	options, logger, err := applyMTU(t, content)
	if err != nil {
		t.Fatal(err)
	}
	if v6 := endpointOptions[option.WireGuardEndpointOptions](t, options, "wg-v6"); v6.MTU != 1200 {
		t.Fatalf("default 1408 → 1280 − 80, got %d", v6.MTU)
	}
	if v4 := endpointOptions[option.WireGuardEndpointOptions](t, options, "wg-v4"); v4.MTU != 1220 {
		t.Fatalf("explicit 1420 → 1280 − 60, got %d", v4.MTU)
	}
	if len(logger.info) != 2 || !strings.Contains(logger.info[0], "wg-v6 mtu 1408 → 1200 (default)") || !strings.Contains(logger.info[1], "wg-v4 mtu 1420 → 1220 clamped") {
		t.Fatalf("%v", logger.info)
	}
}
