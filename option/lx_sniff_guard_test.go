package option

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/schema"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// lx: SPEC 060 — lx.sniff_guard.

func lxSniffGuardDefaultMap(overlay map[string]string) map[string]string {
	result := lxSniffGuardDefaultDetourTypes()
	for key, value := range overlay {
		result[key] = value
	}
	return result
}

func TestLXSniffGuardFullBlock(t *testing.T) {
	t.Parallel()
	options, resolved := mustResolveLX(t, `{"lx":{"sniff_guard":{
		"nodes": {"sni_mismatch": true, "outbound_type": ["vless", "masque"]},
		"detour_type": {"vless": "clean", "*": "suspect"},
		"override": true,
		"tls": {"method": "record_fragment", "delay": "500ms", "persist": true},
		"quic": {"method": "decoy_first_byte", "detour_type": {"socks": "never"}}
	}}}`)
	want := LXSniffGuardResolved{
		SNIMismatchOnly: true,
		OutboundTypes:   []string{"vless", "masque"},
		Override:        true,
		TLS: LXSniffGuardSectionResolved{
			Method:      LXSniffGuardTLSRecordFragment,
			Delay:       500 * time.Millisecond,
			Persist:     true,
			DetourTypes: lxSniffGuardDefaultMap(map[string]string{"vless": "clean"}),
		},
		QUIC: LXSniffGuardSectionResolved{
			Method:      LXSniffGuardQUICDecoy,
			Delay:       LXSniffGuardDefaultDelay,
			DetourTypes: lxSniffGuardDefaultMap(map[string]string{"vless": "clean", "socks": "never"}),
		},
	}
	if got := resolved.SniffGuardOrOff(); !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %+v, want %+v", got, want)
	}
	if options.LX == nil || options.LX.SniffGuard == nil {
		t.Fatal("an enabled block must stay in the canonical options")
	}
	// The canonical form decodes back to the same values, and resolving it is a no-op.
	content, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	_, again := mustResolveLX(t, string(content))
	if !reflect.DeepEqual(again.SniffGuard, want) {
		t.Fatalf("canonical round trip: %s → %+v", content, again.SniffGuard)
	}
}

func TestLXSniffGuardDefaults(t *testing.T) {
	t.Parallel()
	resolved := mustResolveLXResolved(t, `{"lx":{"sniff_guard":{"tls":{"method":"delay"}}}}`).SniffGuard
	if !resolved.Enabled() || !resolved.TLS.Enabled() || resolved.QUIC.Enabled() {
		t.Fatalf("Enabled: %+v", resolved)
	}
	if resolved.SNIMismatchOnly || resolved.OutboundTypes != nil || resolved.Override {
		t.Fatalf("nodes and override must default to no filter, false: %+v", resolved)
	}
	if resolved.TLS.Delay != 300*time.Millisecond || resolved.TLS.Persist {
		t.Fatalf("section defaults: %+v", resolved.TLS)
	}
	if !reflect.DeepEqual(resolved.TLS.DetourTypes, map[string]string{"direct": "clean", "ssh": "clean", "socks": "clean", "*": "suspect"}) {
		t.Fatalf("default map: %v", resolved.TLS.DetourTypes)
	}
	if resolved.QUIC.StartState("direct") != LXSniffGuardNever || resolved.QUIC.StartState("vless") != LXSniffGuardNever {
		t.Fatal("a section without a method must start never on every path")
	}
}

// outbound_type false and an absent key are both "no filter".
func TestLXSniffGuardOutboundTypeNoFilter(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`{"lx":{"sniff_guard":{"nodes":{"outbound_type":false},"tls":{"method":"fragment"}}}}`,
		`{"lx":{"sniff_guard":{"nodes":{},"tls":{"method":"fragment"}}}}`,
		`{"lx":{"sniff_guard":{"nodes":{"outbound_type":null},"tls":{"method":"fragment"}}}}`,
		`{"lx":{"sniff_guard":{"tls":{"method":"fragment"}}}}`,
	} {
		options, resolved := mustResolveLX(t, content)
		guard := resolved.SniffGuard
		if guard.OutboundTypes != nil || !guard.CoversType("vless") || !guard.CoversType("anything") {
			t.Fatalf("%s: expected no filter, got %+v", content, guard)
		}
		if options.LX.SniffGuard.Nodes != nil {
			t.Fatalf("%s: an empty nodes object must canonicalize to absent", content)
		}
	}
	filtered := mustResolveLXResolved(t, `{"lx":{"sniff_guard":{"nodes":{"outbound_type":["masque"]},"quic":{"method":"delay"}}}}`).SniffGuard
	if !filtered.CoversType("masque") || filtered.CoversType("vless") {
		t.Fatalf("CoversType: %+v", filtered)
	}
}

// A block where neither section has a method is an absent block.
func TestLXSniffGuardCanonicalOff(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`{"lx":{"sniff_guard":{}}}`,
		`{"lx":{"sniff_guard":{"tls":{"method":"off"},"quic":{"method":"off"}}}}`,
		`{"lx":{"sniff_guard":{"nodes":{"sni_mismatch":true,"outbound_type":["vless"]},"detour_type":{"vless":"never"},"override":true,"tls":{"delay":"1s","persist":true},"quic":{}}}}`,
	} {
		options, resolved := mustResolveLX(t, content)
		if resolved.SniffGuard.Enabled() || !reflect.DeepEqual(resolved.SniffGuard, LXSniffGuardResolved{}) {
			t.Fatalf("%s: expected the zero policy, got %+v", content, resolved.SniffGuard)
		}
		if options.LX != nil {
			t.Fatalf("%s: a block without methods must canonicalize to absent, got %+v", content, *options.LX)
		}
	}
	if (*LXResolved)(nil).SniffGuardOrOff().Enabled() {
		t.Fatal("a nil *LXResolved must read as off")
	}
}

// A section without a method is dropped from an enabled block, with its keys.
func TestLXSniffGuardCanonicalSection(t *testing.T) {
	t.Parallel()
	options, first := mustResolveLX(t, `{"lx":{"sniff_guard":{"detour_type":{},"tls":{"method":"record_fragment","detour_type":{}},"quic":{"method":"off","persist":true}}}}`)
	content, err := json.Marshal(options.LX)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"sniff_guard":{"tls":{"method":"record_fragment"}}}`; string(content) != want {
		t.Fatalf("canonical %s, want %s", content, want)
	}
	second, err := ResolveLX(&options)
	if err != nil || !reflect.DeepEqual(*second, *first) {
		t.Fatalf("second resolve: %+v %v", second, err)
	}
}

func TestLXSniffGuardStartState(t *testing.T) {
	t.Parallel()
	guard := mustResolveLXResolved(t, `{"lx":{"sniff_guard":{
		"detour_type": {"ssh": "suspect", "vless": "clean", "*": "never"},
		"tls": {"method": "record_fragment", "detour_type": {"vless": "suspect"}},
		"quic": {"method": "decoy_first_byte"}
	}}}`).SniffGuard
	cases := []struct {
		section     LXSniffGuardSectionResolved
		carrierType string
		want        string
	}{
		{guard.TLS, "direct", LXSniffGuardClean},
		{guard.TLS, "socks", LXSniffGuardClean},
		{guard.TLS, "ssh", LXSniffGuardSuspect},
		{guard.TLS, "vless", LXSniffGuardSuspect},
		{guard.TLS, "trojan", LXSniffGuardNever},
		{guard.QUIC, "vless", LXSniffGuardClean},
		{guard.QUIC, "ssh", LXSniffGuardSuspect},
		{guard.QUIC, "hysteria2", LXSniffGuardNever},
	}
	for _, tc := range cases {
		if got := tc.section.StartState(tc.carrierType); got != tc.want {
			t.Fatalf("%s %s: got %s, want %s", tc.section.Method, tc.carrierType, got, tc.want)
		}
	}
	defaults := mustResolveLXResolved(t, `{"lx":{"sniff_guard":{"quic":{"method":"delay","delay":"50ms"}}}}`).SniffGuard.QUIC
	if defaults.StartState("direct") != LXSniffGuardClean || defaults.StartState("vless") != LXSniffGuardSuspect || defaults.Delay != 50*time.Millisecond {
		t.Fatalf("defaults: %+v", defaults)
	}
}

func TestLXSniffGuardRejects(t *testing.T) {
	t.Parallel()
	cases := []struct{ content, want string }{
		{`{"lx":{"sniff_guard":true}}`, "lx.sniff_guard must be an object"},
		{`{"lx":{"sniff_guard":{"mode":"on"}}}`, "lx.sniff_guard"},
		{`{"lx":{"sniff_guard":{"nodes":{"sni":true}}}}`, "lx.sniff_guard.nodes"},
		{`{"lx":{"sniff_guard":{"tls":{"method":"delay","pause":"1s"}}}}`, "lx.sniff_guard.tls"},
		{`{"lx":{"sniff_guard":{"quic":{"method":"delay","pause":"1s"}}}}`, "lx.sniff_guard.quic"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":true}}}}`, "lx.sniff_guard.nodes.outbound_type: true is not a filter; list the types, or use false for no filter"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":[]}}}}`, "lx.sniff_guard.nodes.outbound_type: empty list; list the types, or use false for no filter"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":[ ]},"tls":{"method":"fragment"}}}}`, "lx.sniff_guard.nodes.outbound_type: empty list"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":[""]}}}}`, "lx.sniff_guard.nodes.outbound_type: empty type"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":["urltest"]}}}}`, "lx.sniff_guard.nodes.outbound_type: \"urltest\" is a group"},
		{`{"lx":{"sniff_guard":{"nodes":{"outbound_type":"vless"}}}}`, "lx.sniff_guard.nodes.outbound_type must be a list"},
		{`{"lx":{"sniff_guard":{"nodes":{"sni_mismatch":"yes"}}}}`, "lx.sniff_guard.nodes"},
		{`{"lx":{"sniff_guard":{"detour_type":{"vless":"maybe"}}}}`, `lx.sniff_guard.detour_type.vless must be "never", "clean" or "suspect"`},
		{`{"lx":{"sniff_guard":{"detour_type":{"selector":"clean"}}}}`, `lx.sniff_guard.detour_type: "selector" is a group`},
		{`{"lx":{"sniff_guard":{"detour_type":{"urltest":"clean"}}}}`, `lx.sniff_guard.detour_type: "urltest" is a group`},
		{`{"lx":{"sniff_guard":{"detour_type":{"":"clean"}}}}`, "lx.sniff_guard.detour_type: empty type"},
		{`{"lx":{"sniff_guard":{"quic":{"detour_type":{"socks":"off"}}}}}`, `lx.sniff_guard.quic.detour_type.socks must be`},
		{`{"lx":{"sniff_guard":{"tls":{"detour_type":{"selector":"never"}}}}}`, `lx.sniff_guard.tls.detour_type: "selector" is a group`},
		{`{"lx":{"sniff_guard":{"tls":{"method":"decoy_first_byte"}}}}`, "lx.sniff_guard.tls.method must be one of"},
		{`{"lx":{"sniff_guard":{"quic":{"method":"record_fragment"}}}}`, "lx.sniff_guard.quic.method must be one of"},
		{`{"lx":{"sniff_guard":{"tls":{"method":"delay","delay":"0"}}}}`, "lx.sniff_guard.tls.delay must be > 0"},
		{`{"lx":{"sniff_guard":{"quic":{"method":"delay","delay":"-1s"}}}}`, "lx.sniff_guard.quic.delay must be > 0"},
		{`{"lx":{"sniff_guard":{"tls":"record_fragment"}}}`, "lx.sniff_guard.tls must be an object"},
		{`{"lx":{"sniff_guard":{"override":"yes"}}}`, "lx.sniff_guard"},
	}
	for _, tc := range cases {
		_, err := decodeLXConfig(t, tc.content)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want an error containing %q", tc.content, err, tc.want)
		}
	}
}

// Options built in code skip UnmarshalJSON; ResolveLX validates them again.
func TestLXSniffGuardResolveValidates(t *testing.T) {
	t.Parallel()
	zero := badoption.Duration(0)
	cases := []struct {
		guard LXSniffGuardOptions
		want  string
	}{
		{LXSniffGuardOptions{TLS: &LXSniffGuardTLSOptions{Method: "decoy_first_byte"}}, "lx.sniff_guard.tls.method"},
		{LXSniffGuardOptions{QUIC: &LXSniffGuardQUICOptions{Method: "delay", Delay: &zero}}, "lx.sniff_guard.quic.delay must be > 0"},
		{LXSniffGuardOptions{Nodes: &LXSniffGuardNodesOptions{OutboundType: []string{"selector"}}}, "is a group"},
		{LXSniffGuardOptions{DetourType: LXSniffGuardDetourTypes{"vless": "on"}}, "lx.sniff_guard.detour_type.vless"},
	}
	for _, tc := range cases {
		guard := tc.guard
		options := Options{LX: &LXOptions{SniffGuard: &guard}}
		before := options.LX
		_, err := ResolveLX(&options)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: got %v, want %q", tc.guard, err, tc.want)
		}
		if options.LX != before {
			t.Fatal("a failed resolve must leave the options untouched")
		}
	}
}

func TestLXSniffGuardSchema(t *testing.T) {
	t.Parallel()
	content, err := schema.Generate(context.Background(), reflect.TypeFor[LXOptions]())
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, fragment := range []string{`"sniff_guard"`, `"decoy_first_byte"`, `"record_fragment"`, `"const": false`, `"suspect"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("schema lacks %s", fragment)
		}
	}
}
