// lx:begin awg

package wireguard

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// chainFakeOutbound is a minimal adapter.Outbound for the Start-guard walk;
// only Type/Tag/Dependencies are read.
type chainFakeOutbound struct {
	adapter.Outbound
	tag         string
	outboundTyp string
	detour      string
}

func (o *chainFakeOutbound) Type() string { return o.outboundTyp }
func (o *chainFakeOutbound) Tag() string  { return o.tag }
func (o *chainFakeOutbound) Dependencies() []string {
	if o.detour == "" {
		return nil
	}
	return []string{o.detour}
}

// chainFakeSelector is a group whose current choice is `now` (adapter.OutboundGroup).
type chainFakeSelector struct {
	chainFakeOutbound
	now string
	all []string
}

func (s *chainFakeSelector) Now() string       { return s.now }
func (s *chainFakeSelector) All() []string     { return s.all }
func (s *chainFakeSelector) Network() []string { return nil }

// chainFakeBalanced additionally exposes ActiveTags (the round_robin pool).
type chainFakeBalanced struct {
	chainFakeSelector
	active []string
}

func (s *chainFakeBalanced) ActiveTags() []string { return s.active }

type chainFakeManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *chainFakeManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, ok := m.byTag[tag]
	return ob, ok
}

// TestAWGChainWalk_selectorResolvedThroughNow pins the SPEC 007 restart fix: the
// Start-guard walk descends a selector through its CURRENT choice (the
// cache-restored selection), not stopping at the group and not expanding All().
func TestAWGChainWalk_selectorResolvedThroughNow(t *testing.T) {
	wg := &chainFakeOutbound{tag: "wg-node", outboundTyp: C.TypeWireGuard}
	vless := &chainFakeOutbound{tag: "vless", outboundTyp: C.TypeVLESS}
	sel := &chainFakeSelector{
		chainFakeOutbound: chainFakeOutbound{tag: "proxy", outboundTyp: C.TypeSelector},
		now:               "wg-node",
		all:               []string{"vless", "wg-node"},
	}
	mgr := &chainFakeManager{byTag: map[string]adapter.Outbound{
		"wg-node": wg, "vless": vless, "proxy": sel,
	}}

	// Cached selection = WG member → the chain must be reported blocked.
	if blockedBy := awgDetourChainReachesWireGuard(mgr, "proxy", make(map[string]bool)); blockedBy != "wg-node" {
		t.Fatalf("selector with restored WG selection must block, got %q", blockedBy)
	}

	// Same group, safe current choice → NOT blocked (All() must not be expanded:
	// the WG member is present but not selected).
	sel.now = "vless"
	if blockedBy := awgDetourChainReachesWireGuard(mgr, "proxy", make(map[string]bool)); blockedBy != "" {
		t.Fatalf("selector currently on vless must not block, got %q", blockedBy)
	}
}

// TestAWGChainWalk_balancedResolvedThroughPool: a round_robin urltest is resolved
// through its WHOLE active pool — any WG member in the pool blocks.
func TestAWGChainWalk_balancedResolvedThroughPool(t *testing.T) {
	wg := &chainFakeOutbound{tag: "wg-node", outboundTyp: C.TypeWireGuard}
	v1 := &chainFakeOutbound{tag: "v1", outboundTyp: C.TypeVLESS}
	v2 := &chainFakeOutbound{tag: "v2", outboundTyp: C.TypeVLESS}
	group := &chainFakeBalanced{
		chainFakeSelector: chainFakeSelector{
			chainFakeOutbound: chainFakeOutbound{tag: "balanced", outboundTyp: C.TypeURLTest},
			now:               "v1",
			all:               []string{"v1", "v2", "wg-node"},
		},
		active: []string{"v1", "v2"},
	}
	mgr := &chainFakeManager{byTag: map[string]adapter.Outbound{
		"wg-node": wg, "v1": v1, "v2": v2, "balanced": group,
	}}

	if blockedBy := awgDetourChainReachesWireGuard(mgr, "balanced", make(map[string]bool)); blockedBy != "" {
		t.Fatalf("WG-free pool must not block, got %q", blockedBy)
	}
	group.active = []string{"v1", "wg-node"}
	if blockedBy := awgDetourChainReachesWireGuard(mgr, "balanced", make(map[string]bool)); blockedBy != "wg-node" {
		t.Fatalf("pool containing a WG member must block, got %q", blockedBy)
	}
}

// lx:end awg
