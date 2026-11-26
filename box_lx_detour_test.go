package box

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/chain"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"
)

// lx: SPEC 165 — the pre-pass on option.Options: chain rewrite, path-MTU
// alignment and the changes between the config as written and the effective
// one. No box is started.

func detourTestContext() context.Context {
	ctx := mtuTestContext()
	chain.RegisterOutbound(service.FromContext[adapter.OutboundRegistry](ctx).(*outbound.Registry))
	return ctx
}

// lxbox233 is the shape of LxBox#233: a WG urltest group on position 1 of a
// chain, its members with their own detour through another node.
const lxbox233 = `{
  "endpoints": [
    {"type": "wireguard", "tag": "NL-FREE#114", "detour": "o-ru", "address": ["10.2.0.2/32"],
     "private_key": "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
     "peers": [{"address": "1.2.3.4", "port": 51820, "public_key": "Z1XXLsKYkYxuiYjJIkRvtIKFepCYHTgON+GwPq7SOV4=", "allowed_ips": ["0.0.0.0/0"]}]},
    {"type": "wireguard", "tag": "NL-FREE#115", "detour": "o-ru", "address": ["10.2.0.3/32"], "mtu": 1420,
     "private_key": "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
     "peers": [{"address": "5.6.7.8", "port": 51820, "public_key": "Z1XXLsKYkYxuiYjJIkRvtIKFepCYHTgON+GwPq7SOV4=", "allowed_ips": ["0.0.0.0/0"]}]}
  ],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "vless", "tag": "o-ru", "server": "9.9.9.9", "server_port": 443, "uuid": "bf000d23-0752-40b4-affe-68f7707a9661"},
    {"type": "selector", "tag": "o-direct", "outbounds": ["direct"]},
    {"type": "urltest", "tag": "o-proton", "outbounds": ["NL-FREE#114", "NL-FREE#115"]},
    {"type": "chain", "tag": "c-proton", "outbounds": ["o-direct", "o-proton"]}
  ]%s
}`

// rewriteOverTunnel is Т14: a rewrite of the WG mtu, then the alignment over
// the masque tunnel below.
const rewriteOverTunnel = `{
  "endpoints": [
    {"type": "wireguard", "tag": "wg-exit", "address": ["10.2.0.2/32"], "mtu": 1420,
     "private_key": "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
     "peers": [{"address": "1.2.3.4", "port": 51820, "public_key": "Z1XXLsKYkYxuiYjJIkRvtIKFepCYHTgON+GwPq7SOV4=", "allowed_ips": ["0.0.0.0/0"]}]}
  ],
  "outbounds": [
    {"type": "masque", "tag": "warp", "server": "162.159.192.1", "server_port": 443, "private_key": "AA==", "public_key": "AA==", "ip": "172.16.0.2", "mtu": 1280},
    {"type": "hysteria2", "tag": "hy2", "server": "8.8.8.8", "server_port": 443, "password": "x", "tls": {"enabled": true, "server_name": "a.example"}},
    {"type": "chain", "tag": "virt", "outbounds": ["warp", "wg-exit", "hy2"], "rewrite": {"wireguard": {"mtu": 1300}}}
  ]
}`

type detourRun struct {
	original  string
	effective *option.LXEffectiveConfig
	options   option.Options
	logger    *mtuTestLog
}

func runPrePass(t *testing.T, content string) detourRun {
	t.Helper()
	ctx := detourTestContext()
	var options option.Options
	if err := json.UnmarshalContext(ctx, []byte(content), &options); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Н1: the running config as the daemon captures it.
	running := options
	if _, err := option.ResolveLX(&running); err != nil {
		t.Fatal(err)
	}
	original := encodeOptions(t, running)
	logger := &mtuTestLog{Logger: log.NewNOPFactory().Logger()}
	if _, err := applyLXOptions(ctx, &options, logger); err != nil {
		t.Fatalf("pre-pass: %v", err)
	}
	effective := service.FromContext[*option.LXEffectiveConfig](ctx)
	if effective == nil {
		t.Fatal("no effective config registered")
	}
	if encodeOptions(t, running) != original {
		t.Fatal("the pre-pass wrote through the caller's option structs")
	}
	return detourRun{original: original, effective: effective, options: options, logger: logger}
}

func encodeOptions(t *testing.T, options option.Options) string {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(options); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

type configNodes map[string]map[string]any

func decodeNodes(t *testing.T, content string) configNodes {
	t.Helper()
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
		Endpoints []map[string]any `json:"endpoints"`
	}
	if err := stdjson.Unmarshal([]byte(content), &document); err != nil {
		t.Fatal(err)
	}
	nodes := make(configNodes)
	for _, node := range append(document.Outbounds, document.Endpoints...) {
		nodes[node["tag"].(string)] = node
	}
	return nodes
}

// normalize re-decodes a value so numbers compare as numbers.
func normalize(t *testing.T, value any) any {
	t.Helper()
	data, err := stdjson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := stdjson.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkИ4 applies the changes of every node to its original and compares
// with the effective node.
func checkИ4(t *testing.T, run detourRun) {
	t.Helper()
	content, err := run.effective.Content()
	if err != nil {
		t.Fatal(err)
	}
	original := decodeNodes(t, run.original)
	effective := decodeNodes(t, content)
	for tag, node := range effective {
		if node["type"] == C.TypeChainHop {
			if _, inOriginal := original[tag]; inOriginal {
				t.Fatalf("hop %s in the original config", tag)
			}
			continue
		}
		before, has := original[tag]
		if !has {
			t.Fatalf("node %s missing in the original", tag)
		}
		applied := normalize(t, before).(map[string]any)
		changes := normalize(t, run.effective.Changes(tag))
		var typed []option.LXConfigChange
		data, _ := stdjson.Marshal(changes)
		_ = stdjson.Unmarshal(data, &typed)
		option.ApplyLXConfigChanges(applied, typed)
		if want := normalize(t, node); !reflect.DeepEqual(applied, want) {
			t.Fatalf("И4: %s: changes %s applied to the original give\n%v\nwant\n%v", tag, data, applied, want)
		}
	}
}

func TestPrePassН1Н2И4EveryNode(t *testing.T) {
	for name, content := range map[string]string{
		"lxbox233":           strings.Replace(lxbox233, "%s", "", 1),
		"lxbox233 detour":    strings.Replace(lxbox233, "%s", `, "lx": {"detour": {"mtu_align": "fill", "except_outbounds": ["NL-FREE#115"]}}`, 1),
		"rewrite+align":      rewriteOverTunnel,
		"rewrite zero value": strings.Replace(rewriteOverTunnel, `"rewrite": {"wireguard": {"mtu": 1300}}`, `"rewrite": {"wireguard": {"mtu": 1300}, "hysteria2": {"tls": {"insecure": false, "utls": {"enabled": false}}}}`, 1),
		"issue37":            issue37("2606:4700:104::1", "", ""),
		"issue37 off":        issue37("2606:4700:104::1", "", `, "lx": {"detour": false}`),
		"issue37 explicit":   issue37("2606:4700:104::1", `, "initial_packet_size": 1300`, ""),
		"no chain no tunnel": `{"outbounds": [{"type": "direct", "tag": "direct"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			checkИ4(t, runPrePass(t, content))
		})
	}
}

func TestPrePassТ1LxBox233(t *testing.T) {
	run := runPrePass(t, strings.Replace(lxbox233, "%s", "", 1))
	for _, tag := range []string{"NL-FREE#114", "NL-FREE#115"} {
		changes := run.effective.Changes(tag)
		if len(changes) == 0 || changes[0].Field != "detour" || changes[0].From != "o-ru" || changes[0].To != "c-proton#0" {
			t.Fatalf("Т1: %s changes %+v", tag, changes)
		}
		if got := endpointOptions[option.WireGuardEndpointOptions](t, run.options, tag).Detour; got != "c-proton#0" {
			t.Fatalf("Т1: %s detour %s", tag, got)
		}
	}
	hop := outboundOptions[option.ChainHopOutboundOptions](t, run.options, "c-proton#0")
	if hop.Outbound != "o-direct" || hop.Below != "" {
		t.Fatalf("Т9: hop %+v", hop)
	}
	if changes := run.effective.Changes("o-proton"); len(changes) != 0 {
		t.Fatal("П4: the group is not changed")
	}
	if len(run.logger.warn) != 1 || !strings.Contains(run.logger.warn[0], "o-proton (c-proton #1)") {
		t.Fatalf("Л3: one warning naming the group, got %v", run.logger.warn)
	}
}

func TestPrePassТ14RewriteThenAlign(t *testing.T) {
	run := runPrePass(t, rewriteOverTunnel)
	var mtu []option.LXConfigChange
	for _, change := range run.effective.Changes("wg-exit") {
		if change.Field == "mtu" {
			mtu = append(mtu, change)
		}
	}
	if len(mtu) != 2 || mtu[0].By.Type != option.LXChangeByChainRewrite || mtu[1].By.Type != option.LXChangeByMTUAlign {
		t.Fatalf("Т14: %+v", mtu)
	}
	if normalizeNumber(mtu[0].To) != normalizeNumber(mtu[1].From) {
		t.Fatalf("Н3: from of the second (%v) must be to of the first (%v)", mtu[1].From, mtu[0].To)
	}
	if mtu[1].By.Below != "virt#0" || mtu[1].By.Mode != "clamp" || mtu[1].By.Overhead == 0 || !strings.Contains(mtu[1].By.Reason, "wg-exit mtu") {
		t.Fatalf("М4 by %+v", mtu[1].By)
	}
	// hy2 on position 2 above the WG tunnel: two QUIC fields (М4).
	fields := map[string]bool{}
	for _, change := range run.effective.Changes("hy2") {
		fields[change.Field] = true
	}
	if !fields["detour"] || !fields["initial_packet_size"] || !fields["disable_path_mtu_discovery"] {
		t.Fatalf("М4 QUIC changes %+v", run.effective.Changes("hy2"))
	}
	checkИ4(t, run)
}

func normalizeNumber(value any) string {
	data, _ := stdjson.Marshal(value)
	return string(data)
}

func TestPrePassР9ExpandedDetourAndExcept(t *testing.T) {
	run := runPrePass(t, strings.Replace(lxbox233, "%s", `, "lx": {"detour": {"except_outbounds": ["NL-FREE#115"]}}`, 1))
	content, _ := run.effective.Content()
	for _, key := range []string{`"mtu_align": "clamp"`, `"fragment": true`, `"spoof": true`, `"kernel": true`, `"engine": true`, `"except_outbounds": [`} {
		if !strings.Contains(content, key) {
			t.Fatalf("Р9: effective lx.detour misses %s:\n%s", key, content)
		}
	}
	if !strings.Contains(run.original, `"except_outbounds"`) || strings.Contains(run.original, `"engine"`) {
		t.Fatal("Н1: the running config keeps lx.detour as written")
	}
	// С1/М3: the excepted node is outside the strip set.
	resolved := service.PtrFromContext[option.LXResolved](mustPrePassContext(t, strings.Replace(lxbox233, "%s", `, "lx": {"detour": {"except_outbounds": ["NL-FREE#115"]}}`, 1)))
	if !resolved.DetourNodes["NL-FREE#114"] || resolved.DetourNodes["NL-FREE#115"] || resolved.DetourNodes["o-ru"] {
		t.Fatalf("С1: strip set %v", resolved.DetourNodes)
	}
}

func mustPrePassContext(t *testing.T, content string) context.Context {
	t.Helper()
	ctx := detourTestContext()
	var options option.Options
	if err := json.UnmarshalContext(ctx, []byte(content), &options); err != nil {
		t.Fatal(err)
	}
	ctx, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger())
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestPrePassР8UnknownExcept(t *testing.T) {
	ctx := detourTestContext()
	var options option.Options
	content := strings.Replace(lxbox233, "%s", `, "lx": {"detour": {"mtu_align": "off", "except_outbounds": ["nope"]}}`, 1)
	if err := json.UnmarshalContext(ctx, []byte(content), &options); err != nil {
		t.Fatal(err)
	}
	if _, err := applyLXOptions(ctx, &options, log.NewNOPFactory().Logger()); err == nil || !strings.Contains(err.Error(), "lx.detour.except_outbounds: unknown outbound/endpoint tag: nope") {
		t.Fatalf("Р8: %v", err)
	}
}

func TestPrePassМ3Except(t *testing.T) {
	content := strings.TrimSuffix(strings.TrimSpace(rewriteOverTunnel), "}") + `, "lx": {"detour": {"except_outbounds": ["wg-exit"]}}}`
	run := runPrePass(t, content)
	for _, change := range run.effective.Changes("wg-exit") {
		if change.By.Type == option.LXChangeByMTUAlign {
			t.Fatalf("М3: an excepted node is not aligned: %+v", change)
		}
	}
	checkИ4(t, run)
}

// Д1–Д4: the dial fields upstream ignores under detour leave the effective
// view with an `lx.detour` change, on any node with a detour, whatever
// lx.detour says; no warning; И4 holds.
func TestPrePassД1IgnoredUnderDetour(t *testing.T) {
	for name, root := range map[string]string{"default": "", "detour false": `, "lx": {"detour": false}`} {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(issue37("2606:4700:104::1", `, "tcp_fast_open": true, "routing_mark": 7, "connect_timeout": "5s"`, root), `"server_port": 443,
      "password"`, `"server_port": 443, "bind_interface": "eth0",
      "password"`, 1)
			run := runPrePass(t, content)
			checkИ4(t, run)
			var fields []string
			for _, change := range run.effective.Changes("hy2") {
				if change.By.Type == "lx.detour" { // the contract with clients, not the constant
					if change.To != nil || change.From == nil || !strings.Contains(change.By.Reason, "common/dialer") {
						t.Fatalf("Д1: %+v", change)
					}
					fields = append(fields, change.Field)
				}
			}
			if strings.Join(fields, ",") != "bind_interface,routing_mark,connect_timeout,tcp_fast_open" {
				t.Fatalf("Д1: %v", fields)
			}
			if len(run.logger.warn) != 0 {
				t.Fatalf("Д4: no warning, got %v", run.logger.warn)
			}
			// Upstream gets the node as written; only the effective view
			// leaves the fields out.
			node := outboundOptions[option.Hysteria2OutboundOptions](t, run.options, "hy2")
			if !node.TCPFastOpen || node.RoutingMark != 7 || node.BindInterface != "eth0" {
				t.Fatalf("Д1: the options that go to upstream must stay as written: %+v", node.DialerOptions)
			}
			effective, _ := run.effective.Content()
			if strings.Contains(effective, "tcp_fast_open") || strings.Contains(effective, "routing_mark") {
				t.Fatal("Д1: the effective view leaves the fields out")
			}
			// The node without a detour keeps its fields.
			if changes := run.effective.Changes("warp"); len(changes) != 0 && name == "detour false" {
				t.Fatalf("no detour, no change: %+v", changes)
			}
		})
	}
}

func TestPrePassД3ReadByProtocol(t *testing.T) {
	anytls := &option.AnyTLSOutboundOptions{}
	anytls.Detour = "x"
	anytls.TCPFastOpen = true
	anytls.RoutingMark = 7
	changed, changes, err := withoutIgnoredDialFields(C.TypeAnyTLS, anytls)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "routing_mark" || !changed.(*option.AnyTLSOutboundOptions).TCPFastOpen {
		t.Fatalf("Д3: anytls keeps tcp_fast_open for its own error: %+v", changes)
	}
	if anytls.RoutingMark != 7 {
		t.Fatal("the caller's struct is not written through")
	}
}

func TestPrePassИ5DetourFalse(t *testing.T) {
	ctx := mustPrePassContext(t, strings.Replace(lxbox233, "%s", `, "lx": {"detour": false}`, 1))
	resolved := service.PtrFromContext[option.LXResolved](ctx)
	if resolved.DetourNodes != nil || resolved.Detour.MTUAlign != option.LXMTUAlignOff {
		t.Fatalf("И5: %+v", resolved.Detour)
	}
}

// Д3: impair applies connect_timeout itself, under detour too.
func TestPrePassД3ImpairConnectTimeout(t *testing.T) {
	impair := &option.ImpairOutboundOptions{}
	impair.Detour = "x"
	impair.ConnectTimeout = badoption.Duration(time.Second)
	impair.RoutingMark = 7
	_, changes, err := withoutIgnoredDialFields(C.TypeImpair, impair)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "routing_mark" {
		t.Fatalf("Д3: impair keeps connect_timeout: %+v", changes)
	}
}

// Л3: the warning names the nested groups of a position too.
func TestPrePassЛ3NestedGroupsWarned(t *testing.T) {
	logger := &mtuTestLog{Logger: log.NewNOPFactory().Logger()}
	options := option.Options{Outbounds: []option.Outbound{
		{Type: C.TypeSelector, Tag: "Inner", Options: &option.SelectorOutboundOptions{Outbounds: []string{"x"}}},
		{Type: C.TypeURLTest, Tag: "Dir", Options: &option.URLTestOutboundOptions{Outbounds: []string{"Inner", "y"}}},
		{Type: C.TypeChainHop, Tag: "c#1", Options: &option.ChainHopOutboundOptions{Chain: "c", Position: 1, Outbound: "Dir", Below: "c#0"}},
	}}
	warnChainGroups(&options, logger)
	if len(logger.warn) != 1 || !strings.Contains(logger.warn[0], "Dir (c #1), Inner (c #1)") {
		t.Fatalf("Л3: %v", logger.warn)
	}
}
