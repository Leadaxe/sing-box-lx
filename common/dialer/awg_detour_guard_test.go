// lx:begin awg

package dialer

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// fakeOutbound is a minimal adapter.Outbound for guard tests: only Type()/Tag()
// matter to detourTargetIsWireGuard. The embedded interface is nil — any other
// method would panic, which is fine because the guard never calls them.
type fakeOutbound struct {
	adapter.Outbound
	tag         string
	outboundTyp string
}

func (o *fakeOutbound) Type() string { return o.outboundTyp }
func (o *fakeOutbound) Tag() string  { return o.tag }

// fakeGroup is an adapter.OutboundGroup (selector/urltest stand-in) reporting a
// fixed member-tag list via All().
type fakeGroup struct {
	fakeOutbound
	members []string
}

func (g *fakeGroup) Now() string   { return "" }
func (g *fakeGroup) All() []string { return g.members }

// fakeOutboundManager resolves tags from a fixed map; only Outbound() is used.
type fakeOutboundManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *fakeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, loaded := m.byTag[tag]
	return ob, loaded
}

func wg(tag string) *fakeOutbound    { return &fakeOutbound{tag: tag, outboundTyp: C.TypeWireGuard} }
func vless(tag string) *fakeOutbound { return &fakeOutbound{tag: tag, outboundTyp: C.TypeVLESS} }

func TestDetourTargetIsWireGuard(t *testing.T) {
	// Field matrix (from on-device testing):
	//   AWG detour WG     -> guarded (target is wireguard)
	//   AWG detour AWG    -> guarded (AWG is also type "wireguard")
	//   AWG detour VLESS  -> allowed
	//   WG  detour AWG    -> allowed (owner not AWG; handled by ownerIsAmneziaWG, not here)
	wgNode := wg("wg-out")
	vlessNode := vless("vless-out")

	groupWithWG := &fakeGroup{
		fakeOutbound: fakeOutbound{tag: "sel", outboundTyp: C.TypeSelector},
		members:      []string{"vless-out", "wg-out"},
	}
	groupNoWG := &fakeGroup{
		fakeOutbound: fakeOutbound{tag: "sel2", outboundTyp: C.TypeSelector},
		members:      []string{"vless-out"},
	}
	// Two groups referencing each other — must not loop forever.
	cyclicA := &fakeGroup{
		fakeOutbound: fakeOutbound{tag: "cyc-a", outboundTyp: C.TypeSelector},
		members:      []string{"cyc-b"},
	}
	cyclicB := &fakeGroup{
		fakeOutbound: fakeOutbound{tag: "cyc-b", outboundTyp: C.TypeSelector},
		members:      []string{"cyc-a"},
	}

	mgr := &fakeOutboundManager{byTag: map[string]adapter.Outbound{
		"wg-out":    wgNode,
		"vless-out": vlessNode,
		"sel":       groupWithWG,
		"sel2":      groupNoWG,
		"cyc-a":     cyclicA,
		"cyc-b":     cyclicB,
	}}

	cases := []struct {
		name   string
		target any
		want   bool
	}{
		{"direct wireguard target", wgNode, true},
		{"direct vless target", vlessNode, false},
		{"group containing wireguard", groupWithWG, true},
		{"group without wireguard", groupNoWG, false},
		{"cyclic groups, no wireguard", cyclicA, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detourTargetIsWireGuard(mgr, tc.target, make(map[string]bool))
			if got != tc.want {
				t.Fatalf("detourTargetIsWireGuard(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestDetourGuardInit exercises the full init() path: an AWG owner detouring to
// a wireguard target must cache an error (variant B — every dial fails, the
// instance keeps running), while a non-AWG owner or a non-wireguard target must
// resolve cleanly.
func TestDetourGuardInit(t *testing.T) {
	wgNode := wg("wg-out")
	vlessNode := vless("vless-out")
	mgr := &fakeOutboundManager{byTag: map[string]adapter.Outbound{
		"wg-out":    wgNode,
		"vless-out": vlessNode,
	}}

	t.Run("AWG owner -> WG target is rejected", func(t *testing.T) {
		d := &DetourDialer{outboundManager: mgr, detour: "wg-out", ownerIsAmneziaWG: true}
		_, err := d.Dialer()
		if err == nil {
			t.Fatal("expected AWG-over-wireguard detour to be rejected, got nil error")
		}
	})
	t.Run("AWG owner -> VLESS target is allowed", func(t *testing.T) {
		d := &DetourDialer{outboundManager: mgr, detour: "vless-out", ownerIsAmneziaWG: true}
		if _, err := d.Dialer(); err != nil {
			t.Fatalf("AWG-over-vless detour should be allowed, got: %v", err)
		}
	})
	t.Run("non-AWG owner -> WG target is allowed", func(t *testing.T) {
		d := &DetourDialer{outboundManager: mgr, detour: "wg-out", ownerIsAmneziaWG: false}
		if _, err := d.Dialer(); err != nil {
			t.Fatalf("non-AWG owner detour to wireguard should be allowed, got: %v", err)
		}
	})
}

// lx:end awg
