package lxmtu

import (
	stdjson "encoding/json"
	"strings"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// lx: SPEC 120 §6.1 — formula table, graph walk, modes.

func TestTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		typeName string
		server   string
		overhead int
		known    bool
	}{
		{C.TypeWireGuard, "1.2.3.4", 60, true},
		{C.TypeWireGuard, "2606:4700:104::2", 80, true},
		{C.TypeWireGuard, "wg.example.com", 80, true},
		{C.TypeWireGuard, "", 80, true},
		{C.TypeMASQUE, "162.159.192.1", 79, true},
		{C.TypeMASQUE, "2606:4700:104::2", 99, true},
		{C.TypeMASQUE, "::ffff:1.2.3.4", 79, true},
		{C.TypeOpenVPNClient, "1.2.3.4", 0, false},
		{C.TypeOpenConnect, "1.2.3.4", 0, false},
	}
	for _, tc := range cases {
		overhead, known := TunnelOverhead(tc.typeName, tc.server)
		if overhead != tc.overhead || known != tc.known {
			t.Fatalf("%s/%s: overhead %d known %v, want %d %v", tc.typeName, tc.server, overhead, known, tc.overhead, tc.known)
		}
	}
	if IPUDP("1.2.3.4") != 28 || IPUDP("2001:db8::1") != 48 || IPUDP("example.com") != 48 {
		t.Fatal("ip/udp overhead")
	}
	if TunnelDefaultMTU(C.TypeWireGuard) != 1408 || TunnelDefaultMTU(C.TypeMASQUE) != 1280 || TunnelDefaultMTU(C.TypeOpenVPNClient) != 0 {
		t.Fatal("tunnel defaults")
	}
	for _, typeName := range []string{C.TypeWireGuard, C.TypeMASQUE, C.TypeOpenVPNClient, C.TypeOpenConnect} {
		if !IsTunnelType(typeName) || IsQUICType(typeName) {
			t.Fatal(typeName)
		}
	}
	for _, typeName := range []string{C.TypeHysteria2, C.TypeTUIC, C.TypeHysteria} {
		if IsTunnelType(typeName) || !IsQUICType(typeName) {
			t.Fatal(typeName)
		}
	}
	for limit, want := range map[int]int{1100: 1200, 1200: 1200, 1232: 1232, 1452: 1452, 1500: 1452} {
		if size, raised := QUICClamp(limit); size != want || raised != (limit < 1200) {
			t.Fatalf("clamp %d: %d %v", limit, size, raised)
		}
	}
	for mtu, want := range map[uint32]uint16{1280: 1331, 1420: 1452, 1100: 1200, 0: 1331} {
		if got := MASQUEOuterInitialPacketSize(mtu); got != want {
			t.Fatalf("masque outer %d: %d, want %d", mtu, got, want)
		}
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]Mode{"": ModeClamp, "clamp": ModeClamp, "fill": ModeFill, "off": ModeOff} {
		mode, err := ParseMode(name)
		if err != nil || mode != want {
			t.Fatalf("%q: %v %v", name, mode, err)
		}
		if name != "" && mode.String() != name {
			t.Fatalf("%q round trip: %s", name, mode)
		}
	}
	if _, err := ParseMode("auto"); err == nil || !strings.Contains(err.Error(), `"auto"`) {
		t.Fatalf("unknown mode must be rejected by name, got %v", err)
	}
	policy, err := NewPolicy("fill", []string{"a", "b"})
	if err != nil || policy.Mode != ModeFill || !policy.Excepted("a") || policy.Excepted("c") {
		t.Fatalf("policy %+v %v", policy, err)
	}
}

// The three-mode table of SPEC §2.3: absent → filled, explicit over the limit
// → fill keeps and warns, clamp lowers; explicit under the limit → kept;
// a type default over the limit is lowered in both; except → kept.
func TestApply(t *testing.T) {
	t.Parallel()
	type want struct {
		effective uint32
		changed   bool
		warning   bool
	}
	cases := []struct {
		mode       Mode
		configured uint32
		explicit   bool
		want       want
	}{
		{ModeClamp, 0, false, want{1232, true, false}},
		{ModeFill, 0, false, want{1232, true, false}},
		{ModeOff, 0, false, want{0, false, false}},
		{ModeClamp, 1300, true, want{1232, true, false}},
		{ModeFill, 1300, true, want{1300, false, true}},
		{ModeOff, 1300, true, want{1300, false, false}},
		{ModeClamp, 1232, true, want{1232, false, false}},
		{ModeFill, 1232, true, want{1232, false, false}},
		{ModeClamp, 1200, true, want{1200, false, false}},
		{ModeClamp, 1408, false, want{1232, true, false}},
		{ModeFill, 1408, false, want{1232, true, false}},
	}
	for _, tc := range cases {
		decision := Policy{Mode: tc.mode}.Apply("n", "f", tc.configured, tc.explicit, 1232, "r")
		got := want{decision.Effective, decision.Changed, decision.Warning != ""}
		if got != tc.want {
			t.Fatalf("mode %s configured %d explicit %v: %+v, want %+v", tc.mode, tc.configured, tc.explicit, got, tc.want)
		}
		if decision.Effective > tc.configured && tc.configured != 0 {
			t.Fatalf("a value was raised: %+v", decision)
		}
	}
	excepted := Policy{Mode: ModeClamp, Except: map[string]bool{"n": true}}.Apply("n", "f", 1500, true, 1232, "r")
	if excepted.Changed || excepted.Effective != 1500 || excepted.Warning != "" {
		t.Fatalf("except must keep the value silently: %+v", excepted)
	}
}

func TestDecisionString(t *testing.T) {
	t.Parallel()
	filled := Decision{Tag: "hy2-us", Field: "initial_packet_size", Effective: 1232, Changed: true, Reason: "detour warp[masque] mtu 1280 − 48 ipv6; pmtud off"}
	if got := filled.String(); got != "hy2-us initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)" {
		t.Fatal(got)
	}
	clamped := Decision{Tag: "wg-exit", Field: "mtu", Configured: 1420, Effective: 1200, Explicit: true, Changed: true, Reason: "limited by warp[masque] mtu 1280 via sel − 80 ipv6"}
	if got := clamped.String(); got != "wg-exit mtu 1420 → 1200 clamped (limited by warp[masque] mtu 1280 via sel − 80 ipv6)" {
		t.Fatal(got)
	}
	if clamped.ChainReason() != "clamped: "+clamped.Reason || filled.ChainReason() != "filled: "+filled.Reason {
		t.Fatal("chain reason prefixes")
	}
	kept := Policy{Mode: ModeFill}.Apply("hy2", "initial_packet_size", 1300, true, 1232, "r")
	if !strings.HasPrefix(kept.ChainReason(), "kept (explicit, fill)") {
		t.Fatal(kept.ChainReason())
	}
}

// ---- resolver -----------------------------------------------------------------

type graph struct {
	outbounds []option.Outbound
	endpoints []option.Endpoint
}

func (g *graph) out(tag, typeName string, options any) *graph {
	g.outbounds = append(g.outbounds, option.Outbound{Type: typeName, Tag: tag, Options: options})
	return g
}

func (g *graph) ep(tag, typeName string, options any) *graph {
	g.endpoints = append(g.endpoints, option.Endpoint{Type: typeName, Tag: tag, Options: options})
	return g
}

func (g *graph) align(t *testing.T, policy Policy) []Decision {
	t.Helper()
	decisions, err := NewResolver(g.outbounds, g.endpoints, policy).Align()
	if err != nil {
		t.Fatal(err)
	}
	return decisions
}

func hy2(server, detour string, packetSize int) *option.Hysteria2OutboundOptions {
	options := &option.Hysteria2OutboundOptions{}
	options.Server = server
	options.ServerPort = 443
	options.Detour = detour
	options.InitialPacketSize = packetSize
	return options
}

func masque(server, detour string, mtu uint32) *option.MASQUEOutboundOptions {
	options := &option.MASQUEOutboundOptions{MTU: mtu}
	options.Server = server
	options.Detour = detour
	return options
}

func wg(peer, detour string, mtu uint32) *option.WireGuardEndpointOptions {
	options := &option.WireGuardEndpointOptions{MTU: mtu, Peers: []option.WireGuardPeer{{Address: peer, Port: 51820}}}
	options.Detour = detour
	return options
}

func findDecision(decisions []Decision, tag, field string) *Decision {
	for i := range decisions {
		if decisions[i].Tag == tag && decisions[i].Field == field {
			return &decisions[i]
		}
	}
	return nil
}

func TestResolverQUICOverTunnel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		server string
		below  func(g *graph)
		want   int
	}{
		{"hy2 v6 over masque 1280", "2606:4700:104::2", func(g *graph) { g.out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)) }, 1232},
		{"hy2 v4 over masque 1280", "1.2.3.4", func(g *graph) { g.out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)) }, 1252},
		{"hy2 domain over masque 1280", "hy2.example.com", func(g *graph) { g.out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)) }, 1232},
		{"hy2 v6 over wg 1408", "2606:4700:104::2", func(g *graph) { g.ep("warp", C.TypeWireGuard, wg("1.2.3.4", "", 0)) }, 1360},
		{"hy2 v4 over wg 1420", "1.2.3.4", func(g *graph) { g.ep("warp", C.TypeWireGuard, wg("1.2.3.4", "", 1420)) }, 1392},
		{"hy2 v4 over wg 1500 clamps to 1452", "1.2.3.4", func(g *graph) { g.ep("warp", C.TypeWireGuard, wg("1.2.3.4", "", 1500)) }, 1452},
		{"hy2 v6 over tailscale default", "2606:4700:104::2", func(g *graph) { g.ep("warp", C.TypeTailscale, &option.TailscaleEndpointOptions{}) }, 1232},
		{"hy2 v6 over tailscale system mtu", "2606:4700:104::2", func(g *graph) {
			g.ep("warp", C.TypeTailscale, &option.TailscaleEndpointOptions{SystemInterfaceMTU: 1400})
		}, 1352},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := hy2(tc.server, "warp", 0)
			g := &graph{}
			tc.below(g)
			g.out("hy2", C.TypeHysteria2, node)
			decisions := g.align(t, Policy{})
			if node.InitialPacketSize != tc.want || !node.DisablePathMTUDiscovery {
				t.Fatalf("initial_packet_size %d pmtud-off %v, want %d true", node.InitialPacketSize, node.DisablePathMTUDiscovery, tc.want)
			}
			d := findDecision(decisions, "hy2", "initial_packet_size")
			if d == nil || !d.Changed || d.Explicit || d.Effective != uint32(tc.want) || !strings.Contains(d.Reason, "pmtud off") {
				t.Fatalf("decision %+v", d)
			}
		})
	}
}

func TestResolverTUICAndHysteria(t *testing.T) {
	t.Parallel()
	tuic := &option.TUICOutboundOptions{UDPRelayMode: "quic"}
	tuic.Server = "1.2.3.4"
	tuic.Detour = "warp"
	hy := &option.HysteriaOutboundOptions{}
	hy.Server = "2606:4700:104::2"
	hy.Detour = "warp"
	g := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).out("tuic", C.TypeTUIC, tuic).out("hy", C.TypeHysteria, hy)
	g.align(t, Policy{})
	if tuic.InitialPacketSize != 1252 || !tuic.DisablePathMTUDiscovery || hy.InitialPacketSize != 1232 || !hy.DisablePathMTUDiscovery {
		t.Fatalf("tuic %d hysteria %d", tuic.InitialPacketSize, hy.InitialPacketSize)
	}
}

func TestResolverTunnelOverTunnel(t *testing.T) {
	t.Parallel()
	t.Run("wg v6 peer over masque 1280", func(t *testing.T) {
		exit := wg("2606:4700:104::2", "warp", 0)
		g := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).ep("wg-exit", C.TypeWireGuard, exit)
		decisions := g.align(t, Policy{})
		if exit.MTU != 1200 {
			t.Fatalf("mtu %d", exit.MTU)
		}
		d := findDecision(decisions, "wg-exit", "mtu")
		if d == nil || d.Configured != 1408 || d.Explicit || !d.Changed {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("wg v4 peer over masque 1280", func(t *testing.T) {
		exit := wg("1.2.3.4", "warp", 0)
		(&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).ep("wg-exit", C.TypeWireGuard, exit).align(t, Policy{})
		if exit.MTU != 1220 {
			t.Fatalf("mtu %d", exit.MTU)
		}
	})
	t.Run("masque v6 server over wg 1408", func(t *testing.T) {
		warp := masque("2606:4700:104::2", "wg-in", 0)
		decisions := (&graph{}).ep("wg-in", C.TypeWireGuard, wg("1.2.3.4", "", 0)).out("warp", C.TypeMASQUE, warp).align(t, Policy{})
		d := findDecision(decisions, "warp", "mtu")
		if warp.MTU != 0 || d == nil || d.Changed || d.Effective != 1280 {
			t.Fatalf("default 1280 fits under 1408 − 99 = 1309 and stays absent, got %d %+v", warp.MTU, d)
		}
		warp = masque("2606:4700:104::2", "wg-in", 1340)
		(&graph{}).ep("wg-in", C.TypeWireGuard, wg("1.2.3.4", "", 0)).out("warp", C.TypeMASQUE, warp).align(t, Policy{})
		if warp.MTU != 1309 {
			t.Fatalf("explicit 1340 must clamp to 1309, got %d", warp.MTU)
		}
	})
	t.Run("capacity of a tunnel is its effective mtu", func(t *testing.T) {
		// hy2 → masque (1280 explicit, fits) → wg 1300 v4 peer: masque limit 1300 − 79 = 1221 → hy2 1221 − 48 = 1173 → QUIC minimum.
		node := hy2("2606:4700:104::2", "warp", 0)
		warp := masque("162.159.192.1", "wg-in", 1280)
		decisions := (&graph{}).ep("wg-in", C.TypeWireGuard, wg("1.2.3.4", "", 1300)).out("warp", C.TypeMASQUE, warp).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if warp.MTU != 1221 || node.InitialPacketSize != 1200 {
			t.Fatalf("masque %d hy2 %d", warp.MTU, node.InitialPacketSize)
		}
		d := findDecision(decisions, "hy2", "initial_packet_size")
		if d == nil || !strings.Contains(d.Warning, "QUIC minimum") {
			t.Fatalf("QUIC minimum must warn: %+v", d)
		}
	})
}

func TestResolverGroups(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T, groupType string) (*option.Hysteria2OutboundOptions, []Decision) {
		t.Helper()
		node := hy2("2606:4700:104::2", "sel", 0)
		g := (&graph{}).
			out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).
			ep("wg", C.TypeWireGuard, wg("1.2.3.4", "", 1408)).
			out("vless", C.TypeVLESS, &option.VLESSOutboundOptions{}).
			out("hy2", C.TypeHysteria2, node)
		var group any
		if groupType == C.TypeSelector {
			group = &option.SelectorOutboundOptions{Outbounds: []string{"vless", "wg", "warp"}}
		} else {
			group = &option.URLTestOutboundOptions{Outbounds: []string{"vless", "wg", "warp"}}
		}
		g.out("sel", groupType, group)
		return node, g.align(t, Policy{})
	}
	for _, groupType := range []string{C.TypeSelector, C.TypeURLTest} {
		node, decisions := build(t, groupType)
		if node.InitialPacketSize != 1232 {
			t.Fatalf("%s: min over members must win, got %d", groupType, node.InitialPacketSize)
		}
		d := findDecision(decisions, "hy2", "initial_packet_size")
		if d == nil || !strings.Contains(d.Reason, "warp[masque] mtu 1280 via sel") {
			t.Fatalf("%s: reason must name the limiting member and the group: %+v", groupType, d)
		}
	}
	t.Run("nested groups", func(t *testing.T) {
		node := hy2("1.2.3.4", "outer", 0)
		(&graph{}).
			out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).
			out("inner", C.TypeSelector, &option.SelectorOutboundOptions{Outbounds: []string{"warp"}}).
			out("outer", C.TypeURLTest, &option.URLTestOutboundOptions{Outbounds: []string{"inner"}}).
			out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 1252 {
			t.Fatalf("%d", node.InitialPacketSize)
		}
	})
	t.Run("group of stream proxies is unlimited", func(t *testing.T) {
		node := hy2("1.2.3.4", "sel", 0)
		decisions := (&graph{}).
			out("vless", C.TypeVLESS, &option.VLESSOutboundOptions{}).
			out("sel", C.TypeSelector, &option.SelectorOutboundOptions{Outbounds: []string{"vless", "missing"}}).
			out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 0 || node.DisablePathMTUDiscovery || len(decisions) != 0 {
			t.Fatalf("%+v %v", node.QUICOptions, decisions)
		}
	})
}

func TestResolverTransitiveDetour(t *testing.T) {
	t.Parallel()
	node := hy2("2606:4700:104::2", "vless", 0)
	vless := &option.VLESSOutboundOptions{}
	vless.Detour = "warp"
	decisions := (&graph{}).
		out("hy2", C.TypeHysteria2, node).
		out("vless", C.TypeVLESS, vless).
		out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).align(t, Policy{})
	if node.InitialPacketSize != 1232 {
		t.Fatalf("%d", node.InitialPacketSize)
	}
	d := findDecision(decisions, "hy2", "initial_packet_size")
	if d == nil || !strings.Contains(d.Reason, "warp[masque] mtu 1280 via vless") {
		t.Fatalf("%+v", d)
	}
	// a stream proxy without detour, or with detour direct, is unlimited
	plain := hy2("1.2.3.4", "vless2", 0)
	direct := &option.VLESSOutboundOptions{}
	direct.Detour = "direct"
	(&graph{}).out("hy2", C.TypeHysteria2, plain).out("vless2", C.TypeVLESS, direct).out("direct", C.TypeDirect, &option.DirectOutboundOptions{}).align(t, Policy{})
	if plain.InitialPacketSize != 0 {
		t.Fatal("no tunnel below, nothing to align")
	}
}

func TestResolverCycle(t *testing.T) {
	t.Parallel()
	a := wg("1.2.3.4", "wg-b", 0)
	b := wg("1.2.3.4", "wg-a", 0)
	node := hy2("1.2.3.4", "wg-a", 0)
	// must terminate; the dependency check at start rejects the config
	(&graph{}).ep("wg-a", C.TypeWireGuard, a).ep("wg-b", C.TypeWireGuard, b).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
	self := hy2("1.2.3.4", "hy2", 0)
	(&graph{}).out("hy2", C.TypeHysteria2, self).align(t, Policy{})
	if self.InitialPacketSize != 0 {
		t.Fatal("self detour must be unlimited")
	}
}

func TestResolverExplicitModes(t *testing.T) {
	t.Parallel()
	build := func(packetSize int) (*option.Hysteria2OutboundOptions, *graph) {
		node := hy2("2606:4700:104::2", "warp", packetSize)
		return node, (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).out("hy2", C.TypeHysteria2, node)
	}
	t.Run("clamp lowers", func(t *testing.T) {
		node, g := build(1300)
		decisions := g.align(t, Policy{Mode: ModeClamp})
		if node.InitialPacketSize != 1232 || !node.DisablePathMTUDiscovery {
			t.Fatalf("%+v", node.QUICOptions)
		}
		d := findDecision(decisions, "hy2", "initial_packet_size")
		if d == nil || !d.Explicit || !d.Changed || d.Configured != 1300 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("fill keeps and warns", func(t *testing.T) {
		node, g := build(1300)
		decisions := g.align(t, Policy{Mode: ModeFill})
		if node.InitialPacketSize != 1300 || node.DisablePathMTUDiscovery {
			t.Fatalf("fill must not touch an explicit value or its PMTUD: %+v", node.QUICOptions)
		}
		d := findDecision(decisions, "hy2", "initial_packet_size")
		if d == nil || d.Changed || d.Warning == "" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("fill fills absent", func(t *testing.T) {
		node, g := build(0)
		g.align(t, Policy{Mode: ModeFill})
		if node.InitialPacketSize != 1232 || !node.DisablePathMTUDiscovery {
			t.Fatalf("%+v", node.QUICOptions)
		}
	})
	t.Run("explicit that fits is kept in both", func(t *testing.T) {
		for _, mode := range []Mode{ModeClamp, ModeFill} {
			node, g := build(1232)
			decisions := g.align(t, Policy{Mode: mode})
			if node.InitialPacketSize != 1232 || node.DisablePathMTUDiscovery {
				t.Fatalf("%s: %+v", mode, node.QUICOptions)
			}
			if d := findDecision(decisions, "hy2", "initial_packet_size"); d == nil || d.Changed || d.Warning != "" {
				t.Fatalf("%s: %+v", mode, d)
			}
		}
	})
	t.Run("off touches nothing", func(t *testing.T) {
		node, g := build(1300)
		decisions := g.align(t, Policy{Mode: ModeOff})
		if node.InitialPacketSize != 1300 || node.DisablePathMTUDiscovery || len(decisions) != 0 {
			t.Fatalf("%+v %v", node.QUICOptions, decisions)
		}
		absent, g := build(0)
		g.align(t, Policy{Mode: ModeOff})
		if absent.InitialPacketSize != 0 || absent.DisablePathMTUDiscovery {
			t.Fatalf("%+v", absent.QUICOptions)
		}
	})
	t.Run("except keeps the node", func(t *testing.T) {
		node, g := build(1300)
		decisions := g.align(t, Policy{Mode: ModeClamp, Except: map[string]bool{"hy2": true}})
		if node.InitialPacketSize != 1300 || node.DisablePathMTUDiscovery {
			t.Fatalf("%+v", node.QUICOptions)
		}
		if d := findDecision(decisions, "hy2", "initial_packet_size"); d != nil && (d.Changed || d.Warning != "") {
			t.Fatalf("%+v", d)
		}
		exit := wg("2606:4700:104::2", "warp", 1420)
		(&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).ep("wg-exit", C.TypeWireGuard, exit).align(t, Policy{Except: map[string]bool{"wg-exit": true}})
		if exit.MTU != 1420 {
			t.Fatalf("%d", exit.MTU)
		}
	})
	t.Run("unknown except tag is an error", func(t *testing.T) {
		_, g := build(0)
		_, err := NewResolver(g.outbounds, g.endpoints, Policy{Except: map[string]bool{"nope": true}}).Align()
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("wg explicit mtu in three modes", func(t *testing.T) {
		for mode, want := range map[Mode]uint32{ModeClamp: 1200, ModeFill: 1420, ModeOff: 1420} {
			exit := wg("2606:4700:104::2", "warp", 1420)
			decisions := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).ep("wg-exit", C.TypeWireGuard, exit).align(t, Policy{Mode: mode})
			if exit.MTU != want {
				t.Fatalf("%s: mtu %d, want %d", mode, exit.MTU, want)
			}
			if mode == ModeFill {
				if d := findDecision(decisions, "wg-exit", "mtu"); d == nil || d.Warning == "" {
					t.Fatalf("fill must warn: %+v", d)
				}
			}
		}
	})
}

func TestResolverWarnings(t *testing.T) {
	t.Parallel()
	t.Run("tuic native below is unlimited with a warning", func(t *testing.T) {
		tuic := &option.TUICOutboundOptions{}
		tuic.Server = "1.2.3.4"
		tuic.Detour = "warp"
		node := hy2("1.2.3.4", "tuic", 0)
		decisions := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).out("tuic", C.TypeTUIC, tuic).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 0 {
			t.Fatalf("%d", node.InitialPacketSize)
		}
		d := findDecision(decisions, "hy2", "initial_packet_size")
		if d == nil || d.Changed || !strings.Contains(d.Warning, "native") {
			t.Fatalf("%+v", d)
		}
		// tuic itself is still aligned towards its own detour
		if tuic.InitialPacketSize != 1252 {
			t.Fatalf("tuic %d", tuic.InitialPacketSize)
		}
	})
	t.Run("chain as a detour target", func(t *testing.T) {
		node := hy2("1.2.3.4", "virt", 0)
		decisions := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).out("virt", C.TypeChain, &option.ChainOutboundOptions{Outbounds: []string{"warp", "warp"}}).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 0 {
			t.Fatalf("%d", node.InitialPacketSize)
		}
		if d := findDecision(decisions, "hy2", "initial_packet_size"); d == nil || !strings.Contains(d.Warning, "chain") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("openvpn over a tunnel warns only", func(t *testing.T) {
		ovpn := &option.OpenVPNClientEndpointOptions{}
		ovpn.Server = "1.2.3.4"
		ovpn.Detour = "warp"
		ovpn.MTU = 1500
		oc := &option.OpenConnectEndpointOptions{Server: "vpn.example.com"}
		oc.Detour = "warp"
		decisions := (&graph{}).out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 0)).ep("ovpn", C.TypeOpenVPNClient, ovpn).ep("oc", C.TypeOpenConnect, oc).align(t, Policy{})
		if ovpn.MTU != 1500 {
			t.Fatal("openvpn mtu must not be touched")
		}
		for _, tag := range []string{"ovpn", "oc"} {
			if d := findDecision(decisions, tag, "mtu"); d == nil || d.Changed || !strings.Contains(d.Warning, "unknown") {
				t.Fatalf("%s: %+v", tag, d)
			}
		}
		// openvpn with an explicit mtu is a capacity for nodes above it
		node := hy2("1.2.3.4", "ovpn", 0)
		(&graph{}).ep("ovpn", C.TypeOpenVPNClient, ovpn).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 1452 {
			t.Fatalf("1500 − 28 clamps to 1452, got %d", node.InitialPacketSize)
		}
	})
	t.Run("openvpn without mtu is unlimited", func(t *testing.T) {
		ovpn := &option.OpenVPNClientEndpointOptions{}
		node := hy2("1.2.3.4", "ovpn", 0)
		(&graph{}).ep("ovpn", C.TypeOpenVPNClient, ovpn).out("hy2", C.TypeHysteria2, node).align(t, Policy{})
		if node.InitialPacketSize != 0 {
			t.Fatalf("%d", node.InitialPacketSize)
		}
	})
}

// Decisions come back in config order, tunnels below included, so the log
// reads top-down the way the config does.
func TestResolverDecisionOrder(t *testing.T) {
	t.Parallel()
	decisions := (&graph{}).
		out("hy2", C.TypeHysteria2, hy2("1.2.3.4", "wg-exit", 0)).
		ep("wg-exit", C.TypeWireGuard, wg("1.2.3.4", "warp", 0)).
		out("warp", C.TypeMASQUE, masque("162.159.192.1", "", 1340)).align(t, Policy{})
	if len(decisions) != 2 || decisions[0].Tag != "hy2" || decisions[1].Tag != "wg-exit" {
		t.Fatalf("%+v", decisions)
	}
	if decisions[0].Effective != 1280-28 || decisions[1].Effective != 1280 {
		t.Fatalf("%+v", decisions)
	}
}

// ---- map form (chain) ---------------------------------------------------------

func TestAlignMap(t *testing.T) {
	t.Parallel()
	t.Run("wg v4 peer", func(t *testing.T) {
		m := map[string]any{"peers": []any{map[string]any{"address": "5.6.7.8"}}}
		d := AlignMap(Policy{}, "wg-exit", C.TypeWireGuard, m, 1408, "limited by wg-in(wireguard) mtu 1408")
		if d == nil || d.Configured != 1408 || d.Effective != 1348 || !d.Changed || d.Explicit {
			t.Fatalf("%+v", d)
		}
		if NumberFromMap(m["mtu"]) != 1348 {
			t.Fatalf("%v", m["mtu"])
		}
		if !strings.HasPrefix(d.ChainReason(), "filled: limited by wg-in(wireguard) mtu 1408 − 60 ipv4") {
			t.Fatal(d.ChainReason())
		}
	})
	t.Run("explicit json number clamps", func(t *testing.T) {
		m := map[string]any{"mtu": stdjson.Number("1408"), "peers": []any{map[string]any{"address": "wg.example.com"}}}
		d := AlignMap(Policy{}, "wg-exit", C.TypeWireGuard, m, 1280, "r")
		if d.Effective != 1200 || !d.Explicit || NumberFromMap(m["mtu"]) != 1200 || !strings.HasPrefix(d.ChainReason(), "clamped:") {
			t.Fatalf("%+v %v", d, m["mtu"])
		}
		fill := map[string]any{"mtu": float64(1408), "peers": []any{map[string]any{"address": "wg.example.com"}}}
		d = AlignMap(Policy{Mode: ModeFill}, "wg-exit", C.TypeWireGuard, fill, 1280, "r")
		if d.Effective != 1408 || d.Changed || d.Warning == "" || NumberFromMap(fill["mtu"]) != 1408 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("masque v6 server", func(t *testing.T) {
		m := map[string]any{"server": "2606:4700:104::2"}
		d := AlignMap(Policy{}, "warp", C.TypeMASQUE, m, 1300, "r")
		if d.Effective != 1201 || NumberFromMap(m["mtu"]) != 1201 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("unlimited keeps and carries the reason", func(t *testing.T) {
		m := map[string]any{"mtu": 1408}
		d := AlignMap(Policy{}, "wg-exit", C.TypeWireGuard, m, Unlimited, "warning: tuic")
		if d.Changed || d.Effective != 1408 || d.Reason != "warning: tuic" || NumberFromMap(m["mtu"]) != 1408 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("openvpn warns", func(t *testing.T) {
		m := map[string]any{"mtu": 1500, "server": "1.2.3.4"}
		d := AlignMap(Policy{}, "ovpn", C.TypeOpenVPNClient, m, 1280, "r")
		if d.Changed || d.Warning == "" || NumberFromMap(m["mtu"]) != 1500 {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("hysteria2 link", func(t *testing.T) {
		m := map[string]any{"server": "2606:4700:104::2"}
		d := AlignMap(Policy{}, "hy2", C.TypeHysteria2, m, 1408, "limited by wg-in(wireguard) mtu 1408")
		if d == nil || d.Effective != 1360 || !d.Changed || NumberFromMap(m["initial_packet_size"]) != 1360 || m["disable_path_mtu_discovery"] != true {
			t.Fatalf("%+v %v", d, m)
		}
		explicit := map[string]any{"server": "1.2.3.4", "initial_packet_size": stdjson.Number("1300"), "disable_path_mtu_discovery": false}
		d = AlignMap(Policy{Mode: ModeFill}, "hy2", C.TypeHysteria2, explicit, 1280, "r")
		if d.Changed || explicit["disable_path_mtu_discovery"] != false || NumberFromMap(explicit["initial_packet_size"]) != 1300 || d.Warning == "" {
			t.Fatalf("%+v %v", d, explicit)
		}
		d = AlignMap(Policy{}, "hy2", C.TypeHysteria2, explicit, 1280, "r")
		if !d.Changed || d.Effective != 1252 || explicit["disable_path_mtu_discovery"] != true {
			t.Fatalf("%+v %v", d, explicit)
		}
	})
	t.Run("QUIC minimum", func(t *testing.T) {
		m := map[string]any{"server": "2606:4700:104::2"}
		d := AlignMap(Policy{}, "hy2", C.TypeHysteria2, m, 1240, "r")
		if d.Effective != 1200 || !strings.Contains(d.Warning, "QUIC minimum") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("other types", func(t *testing.T) {
		if d := AlignMap(Policy{}, "vless", C.TypeVLESS, map[string]any{}, 1280, "r"); d != nil {
			t.Fatalf("%+v", d)
		}
	})
	if NumberFromMap(stdjson.Number("1.5e3")) != 1500 || NumberFromMap(int64(7)) != 7 || NumberFromMap(uint32(9)) != 9 || NumberFromMap("x") != 0 {
		t.Fatal("number forms")
	}
}
