// lx:begin awg

package group

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// fakeOutbound is a minimal adapter.Outbound; only Type/Tag/Dependencies are read.
type fakeOutbound struct {
	adapter.Outbound
	tag         string
	outboundTyp string
	detour      string
}

func (o *fakeOutbound) Type() string { return o.outboundTyp }
func (o *fakeOutbound) Tag() string  { return o.tag }
func (o *fakeOutbound) Dependencies() []string {
	if o.detour == "" {
		return nil
	}
	return []string{o.detour}
}

// fakeAWG implements adapter.AmneziaWGSuspendable and records suspension.
type fakeAWG struct {
	fakeOutbound
	awg       bool
	suspended bool
}

func (a *fakeAWG) IsAmneziaWG() bool { return a.awg }
func (a *fakeAWG) SuspendAmneziaWG() { a.suspended = true }

// fakeManager resolves tags and reverse-deps (ConsumersOf) from fixed maps.
type fakeManager struct {
	adapter.OutboundManager
	byTag     map[string]adapter.Outbound
	consumers map[string][]string
}

func (m *fakeManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, ok := m.byTag[tag]
	return ob, ok
}
func (m *fakeManager) ConsumersOf(tag string) []string { return m.consumers[tag] }

func TestChainReachesWireGuard(t *testing.T) {
	wg := &fakeOutbound{tag: "wg", outboundTyp: C.TypeWireGuard}
	vlessToWG := &fakeOutbound{tag: "v2wg", outboundTyp: C.TypeVLESS, detour: "wg"}
	vlessLeaf := &fakeOutbound{tag: "vleaf", outboundTyp: C.TypeVLESS}
	mgr := &fakeManager{byTag: map[string]adapter.Outbound{
		"wg": wg, "v2wg": vlessToWG, "vleaf": vlessLeaf,
	}}

	if !chainReachesWireGuard(mgr, wg, map[string]bool{}) {
		t.Fatal("direct wireguard member must reach wireguard")
	}
	if !chainReachesWireGuard(mgr, vlessToWG, map[string]bool{}) {
		t.Fatal("vless detouring to wireguard must reach wireguard")
	}
	if chainReachesWireGuard(mgr, vlessLeaf, map[string]bool{}) {
		t.Fatal("plain vless must not reach wireguard")
	}
}

func TestSuspendAmneziaWGConsumers(t *testing.T) {
	// awg-direct detours through the group "sel"
	awgDirect := &fakeAWG{fakeOutbound: fakeOutbound{tag: "awg-direct", outboundTyp: C.TypeWireGuard, detour: "sel"}, awg: true}
	// awg-via-hop -> vless-hop -> sel
	awgViaHop := &fakeAWG{fakeOutbound: fakeOutbound{tag: "awg-hop", outboundTyp: C.TypeWireGuard, detour: "vless-hop"}, awg: true}
	vlessHop := &fakeOutbound{tag: "vless-hop", outboundTyp: C.TypeVLESS, detour: "sel"}
	// plain-wg detours through sel but is NOT amneziawg — must stay untouched
	plainWG := &fakeAWG{fakeOutbound: fakeOutbound{tag: "plain-wg", outboundTyp: C.TypeWireGuard, detour: "sel"}, awg: false}

	mgr := &fakeManager{
		byTag: map[string]adapter.Outbound{
			"awg-direct": awgDirect, "awg-hop": awgViaHop,
			"vless-hop": vlessHop, "plain-wg": plainWG,
		},
		consumers: map[string][]string{
			"sel":       {"awg-direct", "vless-hop", "plain-wg"},
			"vless-hop": {"awg-hop"},
		},
	}

	suspendAmneziaWGConsumers(mgr, "sel", map[string]bool{})

	if !awgDirect.suspended {
		t.Error("direct AmneziaWG consumer must be suspended")
	}
	if !awgViaHop.suspended {
		t.Error("transitive AmneziaWG consumer (via vless hop) must be suspended")
	}
	if plainWG.suspended {
		t.Error("plain (non-AmneziaWG) wireguard consumer must NOT be suspended")
	}
}

// A round_robin pool rebuild whose new pool contains a WireGuard-based member
// must suspend AWG consumers of the group; a WG-free pool must not.
func TestSuspendAmneziaWGConsumersOnPool(t *testing.T) {
	wg := &fakeOutbound{tag: "wg", outboundTyp: C.TypeWireGuard}
	vless1 := &fakeOutbound{tag: "v1", outboundTyp: C.TypeVLESS}
	vless2 := &fakeOutbound{tag: "v2", outboundTyp: C.TypeVLESS}
	awg := &fakeAWG{fakeOutbound: fakeOutbound{tag: "awg", outboundTyp: C.TypeWireGuard, detour: "balanced"}, awg: true}
	mgr := &fakeManager{
		byTag:     map[string]adapter.Outbound{"wg": wg, "v1": vless1, "v2": vless2, "awg": awg},
		consumers: map[string][]string{"balanced": {"awg"}},
	}

	suspendAmneziaWGConsumersOnPool(mgr, "balanced", []string{"v1", "v2"})
	if awg.suspended {
		t.Fatal("a WG-free pool must not suspend AWG consumers")
	}
	suspendAmneziaWGConsumersOnPool(mgr, "balanced", []string{"v1", "wg", "v2"})
	if !awg.suspended {
		t.Fatal("a pool containing a WireGuard member must suspend AWG consumers of the group")
	}
}

// A switch to a non-wireguard member must suspend nothing.
func TestSuspendSkippedForNonWireGuardSwitch(t *testing.T) {
	awg := &fakeAWG{fakeOutbound: fakeOutbound{tag: "awg", outboundTyp: C.TypeWireGuard, detour: "sel"}, awg: true}
	vlessLeaf := &fakeOutbound{tag: "vleaf", outboundTyp: C.TypeVLESS}
	mgr := &fakeManager{
		byTag:     map[string]adapter.Outbound{"awg": awg, "vleaf": vlessLeaf},
		consumers: map[string][]string{"sel": {"awg"}},
	}

	// selected member is plain vless (does not reach wireguard) → no suspension
	suspendAmneziaWGConsumersOnWireGuardSwitch(mgr, "sel", vlessLeaf)
	if awg.suspended {
		t.Error("must not suspend when the selected member does not reach wireguard")
	}
}

// lx:end awg
