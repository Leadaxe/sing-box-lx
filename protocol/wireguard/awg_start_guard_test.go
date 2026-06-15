// lx:begin awg

package wireguard

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// fakeOutbound is a minimal adapter.Outbound for the start-guard chain walk:
// only Type() and Dependencies() (the detour) are consulted. The embedded
// interface is nil — any other method would panic, which never happens here.
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

// fakeGroup is an adapter.OutboundGroup (selector/urltest stand-in); the chain
// walk must stop at it without expanding All().
type fakeGroup struct {
	fakeOutbound
	members []string
}

func (g *fakeGroup) Now() string   { return "" }
func (g *fakeGroup) All() []string { return g.members }

type fakeOutboundManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *fakeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, loaded := m.byTag[tag]
	return ob, loaded
}

func TestAwgDetourChainReachesWireGuard(t *testing.T) {
	mgr := &fakeOutboundManager{byTag: map[string]adapter.Outbound{
		// AWG -> wg-out (direct)
		"wg-out": &fakeOutbound{tag: "wg-out", outboundTyp: C.TypeWireGuard},
		// AWG -> vless-hop -> wg-deep (transitive)
		"vless-hop": &fakeOutbound{tag: "vless-hop", outboundTyp: C.TypeVLESS, detour: "wg-deep"},
		"wg-deep":   &fakeOutbound{tag: "wg-deep", outboundTyp: C.TypeWireGuard},
		// AWG -> vless-leaf -> direct-leaf (no wireguard anywhere)
		"vless-leaf":  &fakeOutbound{tag: "vless-leaf", outboundTyp: C.TypeVLESS, detour: "direct-leaf"},
		"direct-leaf": &fakeOutbound{tag: "direct-leaf", outboundTyp: C.TypeDirect},
		// AWG -> sel (selector hiding a wireguard member) — walk must stop, return ""
		"sel": &fakeGroup{
			fakeOutbound: fakeOutbound{tag: "sel", outboundTyp: C.TypeSelector},
			members:      []string{"wg-out"},
		},
		// cyclic detour: a -> b -> a, no wireguard
		"cyc-a": &fakeOutbound{tag: "cyc-a", outboundTyp: C.TypeVLESS, detour: "cyc-b"},
		"cyc-b": &fakeOutbound{tag: "cyc-b", outboundTyp: C.TypeVLESS, detour: "cyc-a"},
	}}

	cases := []struct {
		name      string
		start     string
		wantEmpty bool
		wantTag   string
	}{
		{"direct wireguard", "wg-out", false, "wg-out"},
		{"transitive via vless", "vless-hop", false, "wg-deep"},
		{"no wireguard in chain", "vless-leaf", true, ""},
		{"selector in the middle is skipped", "sel", true, ""},
		{"cyclic chain terminates", "cyc-a", true, ""},
		{"unknown tag", "nope", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := awgDetourChainReachesWireGuard(mgr, tc.start, make(map[string]bool))
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("expected no wireguard in chain, got %q", got)
				}
				return
			}
			if got != tc.wantTag {
				t.Fatalf("expected blocked-by %q, got %q", tc.wantTag, got)
			}
		})
	}
}

// lx:end awg
