package option

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json"
)

// lx: SPEC 098 — the root `lx` block.

func decodeLXConfig(t *testing.T, content string) (Options, error) {
	t.Helper()
	var options Options
	err := json.UnmarshalContext(context.Background(), []byte(content), &options)
	return options, err
}

func mustDecodeLXConfig(t *testing.T, content string) Options {
	t.Helper()
	options, err := decodeLXConfig(t, content)
	if err != nil {
		t.Fatalf("decode %s: %v", content, err)
	}
	return options
}

func mustResolveLXResolved(t *testing.T, content string) *LXResolved {
	t.Helper()
	_, resolved := mustResolveLX(t, content)
	return resolved
}

func mustResolveLX(t *testing.T, content string) (Options, *LXResolved) {
	t.Helper()
	options := mustDecodeLXConfig(t, content)
	resolved, err := ResolveLX(&options)
	if err != nil {
		t.Fatalf("resolve %s: %v", content, err)
	}
	return options, resolved
}

func TestLXBlockParsesEveryKey(t *testing.T) {
	t.Parallel()
	_, resolved := mustResolveLX(t, `{"lx": {
		"wg": {
			"idle_suspend": "30s",
			"idle_suspend_reachable": "5m",
			"idle_teardown": "10m",
			"lazy_build": true,
			"build_max": 4,
			"build_overflow": "build"
		},
		"masque": {"idle_timeout": "2m"},
		"trusttunnel": {"idle_timeout": "3m"}
	}}`)
	want := LXResolved{
		WG: LXWGResolved{
			IdleSuspend:          30 * time.Second,
			IdleSuspendReachable: 5 * time.Minute,
			IdleTeardown:         10 * time.Minute,
			IdleTeardownSet:      true,
			LazyBuild:            true,
			BuildMax:             4,
			BuildOverflow:        LXBuildOverflowBuild,
		},
		MASQUE:         LXMASQUEResolved{IdleTimeout: 2 * time.Minute},
		TrustTunnel:    LXTrustTunnelResolved{IdleTimeout: 3 * time.Minute},
		MTUAlign:       LXMTUAlignResolved{Mode: LXMTUAlignClamp},
		QUICSniffDecoy: resolveLXQUICSniffDecoy(nil),
	}
	if !reflect.DeepEqual(*resolved, want) {
		t.Fatalf("resolved = %+v, want %+v", *resolved, want)
	}
}

func TestLXBlockEmptyEqualsAbsent(t *testing.T) {
	t.Parallel()
	for _, content := range []string{`{}`, `{"lx": {}}`, `{"lx": {"wg": {}, "masque": {}, "trusttunnel": {}, "mtu_align": {}}}`, `{"lx": {"mtu_align": ""}}`} {
		options, resolved := mustResolveLX(t, content)
		if !reflect.DeepEqual(*resolved, lxDefaults()) {
			t.Fatalf("%s: expected all-off, got %+v", content, *resolved)
		}
		if options.LX != nil {
			t.Fatalf("%s: an empty block must canonicalize to absent, got %+v", content, *options.LX)
		}
	}
}

// lxDefaults is what an absent block resolves to: every knob off, mtu_align
// at its default (lx: SPEC 120), quic_sniff_decoy off with its default lists
// (lx: SPEC 151).
func lxDefaults() LXResolved {
	return LXResolved{MTUAlign: LXMTUAlignResolved{Mode: LXMTUAlignClamp}, QUICSniffDecoy: resolveLXQUICSniffDecoy(nil)}
}

func TestLXResolvedNilIsAllOff(t *testing.T) {
	t.Parallel()
	var resolved *LXResolved
	if resolved.WGOrZero() != (LXWGResolved{}) || resolved.MASQUEOrZero() != (LXMASQUEResolved{}) ||
		resolved.TrustTunnelOrZero() != (LXTrustTunnelResolved{}) {
		t.Fatal("a nil *LXResolved must read as all-off")
	}
	if !reflect.DeepEqual(resolved.MTUAlignOrDefault(), LXMTUAlignResolved{Mode: LXMTUAlignClamp}) {
		t.Fatal("a nil *LXResolved must read as mtu_align clamp")
	}
}

// Unknown keys are errors, not silent defaults — including the reserved
// `naive` sub-block, which has no parser until SPEC 096.
func TestLXBlockRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`{"lx": {"naive": {"single_engine": true}}}`,
		`{"lx": {"wg": {"idle_supend": "30s"}}}`,
		`{"lx": {"masque": {"idle": "5m"}}}`,
		`{"lx": {"trusttunnel": {"idle": "5m"}}}`,
		`{"lx": {"foo": 1}}`,
		// the route.lx_idle_* aliases are gone (owner 2026-11-02)
		`{"route": {"lx_idle_suspend": "30s"}}`,
		`{"route": {"lx_idle_suspend_reachable": "5m"}}`,
		`{"route": {"lx_idle_teardown": "0"}}`,
	} {
		if _, err := decodeLXConfig(t, content); err == nil {
			t.Fatalf("%s: an unknown key must be rejected", content)
		}
	}
}

func TestLXBlockValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		content string
		want    string
	}{
		{`{"lx":{"wg":{"idle_suspend":"-1s"}}}`, "lx.wg.idle_suspend must be >= 0"},
		{`{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"-1s"}}}`, "lx.wg.idle_suspend_reachable must be >= 0"},
		{`{"lx":{"wg":{"idle_suspend":"30s","idle_teardown":"-1s"}}}`, "lx.wg.idle_teardown must be >= 0"},
		{`{"lx":{"wg":{"idle_suspend_reachable":"5m"}}}`, "lx.wg.idle_suspend_reachable requires lx.wg.idle_suspend"},
		{`{"lx":{"wg":{"idle_teardown":"10m"}}}`, "lx.wg.idle_teardown requires lx.wg.idle_suspend"},
		{`{"lx":{"wg":{"idle_teardown":"0"}}}`, "lx.wg.idle_teardown requires lx.wg.idle_suspend"},
		{`{"lx":{"wg":{"lazy_build":true}}}`, "lx.wg.lazy_build requires lx.wg.idle_suspend"},
		{`{"lx":{"wg":{"idle_suspend":"5m","idle_suspend_reachable":"30s"}}}`, "lx.wg.idle_suspend_reachable must be >= lx.wg.idle_suspend"},
		{`{"lx":{"wg":{"build_max":-1}}}`, "lx.wg.build_max must be >= 0"},
		{`{"lx":{"wg":{"build_overflow":"drop"}}}`, `lx.wg.build_overflow must be "wait" or "build"`},
		{`{"lx":{"masque":{"idle_timeout":"-1s"}}}`, "lx.masque.idle_timeout must be >= 0"},
		{`{"lx":{"trusttunnel":{"idle_timeout":"-1s"}}}`, "lx.trusttunnel.idle_timeout must be >= 0"},
		{`{"lx":{"mtu_align":{"except":[""]}}}`, "lx.mtu_align.except: empty tag"},
	}
	for _, tc := range cases {
		options := mustDecodeLXConfig(t, tc.content)
		before := options
		_, err := ResolveLX(&options)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: expected %q, got %v", tc.content, tc.want, err)
		}
		if options.LX != before.LX {
			t.Fatalf("%s: a failed resolve must leave the options untouched", tc.content)
		}
	}
}

// Rules that must NOT fire: build_max and build_overflow stand alone, an
// explicit "wait" is the default, and a reachable window equal to the suspend
// threshold is allowed.
func TestLXBlockValidationAccepts(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`{"lx":{"wg":{"build_max":0}}}`,
		`{"lx":{"wg":{"build_max":2,"build_overflow":"wait"}}}`,
		`{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"30s"}}}`,
		`{"lx":{"wg":{"idle_suspend":"30s","idle_teardown":"0","lazy_build":true}}}`,
		`{"lx":{"masque":{"idle_timeout":"0"}}}`,
		`{"lx":{"trusttunnel":{"idle_timeout":"0"}}}`,
	} {
		mustResolveLX(t, content)
	}
}

// idle_teardown: absent inherits idle_suspend_reachable, an explicit value
// wins, an explicit "0" disables teardown and survives decoding as a value.
func TestLXWGIdleTeardown(t *testing.T) {
	t.Parallel()
	_, inherited := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m"}}}`)
	if inherited.WG.IdleTeardown != 5*time.Minute || inherited.WG.IdleTeardownSet {
		t.Fatalf("absent teardown must inherit the reachable window, got %+v", inherited.WG)
	}
	_, explicit := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"1h"}}}`)
	if explicit.WG.IdleTeardown != time.Hour || !explicit.WG.IdleTeardownSet {
		t.Fatalf("explicit teardown must win, got %+v", explicit.WG)
	}
	options, disabled := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"0"}}}`)
	if disabled.WG.IdleTeardown != 0 || !disabled.WG.IdleTeardownSet {
		t.Fatalf(`explicit "0" must disable teardown and stay a value, got %+v`, disabled.WG)
	}
	if options.LX.WG.IdleTeardown == nil || *options.LX.WG.IdleTeardown != 0 {
		t.Fatal(`explicit "0" must stay present in the canonical block`)
	}
	_, off := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s"}}}`)
	if off.WG.IdleTeardown != 0 || off.WG.IdleTeardownSet {
		t.Fatalf("no reachable window, no teardown key → teardown off, got %+v", off.WG)
	}
}

// The canonical form marshals and decodes back to the same values.
func TestLXCanonicalForm(t *testing.T) {
	t.Parallel()
	options, resolved := mustResolveLX(t, `{"route":{"final":"direct"},"lx":{"wg":{
		"idle_suspend": "30s",
		"idle_suspend_reachable": "5m",
		"idle_teardown": "0"
	}}}`)
	content, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, fragment := range []string{`"lx":{"wg":{`, `"idle_suspend":"30s"`, `"idle_suspend_reachable":"5m0s"`, `"idle_teardown":"0s"`, `"final":"direct"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("canonical form lacks %s: %s", fragment, text)
		}
	}
	roundTrip, err := decodeLXConfig(t, text)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ResolveLX(&roundTrip)
	if err != nil || !reflect.DeepEqual(*again, *resolved) {
		t.Fatalf("canonical round trip: %+v %v", again, err)
	}
}

// Resolving twice is a no-op: same values, and the options are not rewritten.
func TestLXResolveIdempotent(t *testing.T) {
	t.Parallel()
	options, first := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s"},"masque":{"idle_timeout":"5m"}}}`)
	lx := options.LX
	second, err := ResolveLX(&options)
	if err != nil || !reflect.DeepEqual(*second, *first) {
		t.Fatalf("second resolve: %+v %v", second, err)
	}
	if *options.LX.WG != *lx.WG || *options.LX.MASQUE != *lx.MASQUE {
		t.Fatal("a second resolve must not change the canonical options")
	}
}

// ResolveLX replaces the LX pointer instead of writing through it: resolving a
// shallow copy (the running-config snapshot does this) must leave the original
// options as they were.
func TestLXResolveDoesNotWriteThrough(t *testing.T) {
	t.Parallel()
	original := mustDecodeLXConfig(t, `{"lx":{"wg":{"build_max":2},"trusttunnel":{}}}`)
	snapshot := original
	if _, err := ResolveLX(&snapshot); err != nil {
		t.Fatal(err)
	}
	if original.LX.TrustTunnel == nil {
		t.Fatal("resolving a copy wrote into the original's lx block")
	}
	if snapshot.LX.TrustTunnel != nil || snapshot.LX.WG.BuildMax != 2 {
		t.Fatalf("the copy must be canonical, got %+v", *snapshot.LX)
	}
}

// MASQUE node idle_timeout: an explicit "0" must decode as present, so that it
// can override the global default.
func TestMASQUEIdleTimeoutZeroIsAValue(t *testing.T) {
	t.Parallel()
	var zero MASQUEOutboundOptions
	if err := json.Unmarshal([]byte(`{"idle_timeout":"0"}`), &zero); err != nil {
		t.Fatal(err)
	}
	if zero.IdleTimeout == nil || *zero.IdleTimeout != 0 {
		t.Fatal(`an explicit "0" must decode as a present zero`)
	}
	var absent MASQUEOutboundOptions
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.IdleTimeout != nil {
		t.Fatal("an omitted idle_timeout must decode as absent")
	}
}

// lx: SPEC 120 — lx.mtu_align: a mode string or {mode, except}; absent,
// empty and "" all mean clamp; an unknown mode is rejected by key name; the
// canonical form round-trips.
func TestLXMTUAlign(t *testing.T) {
	t.Parallel()
	cases := []struct {
		content string
		want    LXMTUAlignResolved
	}{
		{`{}`, LXMTUAlignResolved{Mode: "clamp"}},
		{`{"lx":{"mtu_align":"clamp"}}`, LXMTUAlignResolved{Mode: "clamp"}},
		{`{"lx":{"mtu_align":"fill"}}`, LXMTUAlignResolved{Mode: "fill"}},
		{`{"lx":{"mtu_align":"off"}}`, LXMTUAlignResolved{Mode: "off"}},
		{`{"lx":{"mtu_align":{"mode":"fill"}}}`, LXMTUAlignResolved{Mode: "fill"}},
		{`{"lx":{"mtu_align":{"except":["my-wg"]}}}`, LXMTUAlignResolved{Mode: "clamp", Except: []string{"my-wg"}}},
		{`{"lx":{"mtu_align":{"except":"my-wg"}}}`, LXMTUAlignResolved{Mode: "clamp", Except: []string{"my-wg"}}},
		{`{"lx":{"mtu_align":{"mode":"off","except":["a","b"]}}}`, LXMTUAlignResolved{Mode: "off", Except: []string{"a", "b"}}},
	}
	for _, tc := range cases {
		options, resolved := mustResolveLX(t, tc.content)
		if !reflect.DeepEqual(resolved.MTUAlign, tc.want) {
			t.Fatalf("%s: %+v, want %+v", tc.content, resolved.MTUAlign, tc.want)
		}
		if !reflect.DeepEqual(resolved.MTUAlignOrDefault(), tc.want) {
			t.Fatalf("%s: accessor %+v", tc.content, resolved.MTUAlignOrDefault())
		}
		// the canonical form decodes back to the same values
		content, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ResolveLX(common.Ptr(mustDecodeLXConfig(t, string(content))))
		if err != nil || !reflect.DeepEqual(again.MTUAlign, tc.want) {
			t.Fatalf("%s: round trip %s → %+v %v", tc.content, content, again, err)
		}
	}
	for _, content := range []string{
		`{"lx":{"mtu_align":"auto"}}`,
		`{"lx":{"mtu_align":{"mode":"auto"}}}`,
		`{"lx":{"mtu_align":{"mode":"clamp","exclude":["a"]}}}`,
		`{"lx":{"mtu_align":1}}`,
	} {
		if _, err := decodeLXConfig(t, content); err == nil {
			t.Fatalf("%s: must be rejected", content)
		} else if strings.Contains(content, "auto") && !strings.Contains(err.Error(), "lx.mtu_align.mode") {
			t.Fatalf("%s: the error must name the key, got %v", content, err)
		}
	}
	// marshal: string form without exceptions, object form with them
	short, _ := json.Marshal(LXMTUAlign{Mode: "fill"})
	long, _ := json.Marshal(LXMTUAlign{Mode: "fill", Except: []string{"a", "b"}})
	if string(short) != `"fill"` || string(long) != `{"mode":"fill","except":["a","b"]}` {
		t.Fatalf("%s %s", short, long)
	}
	// the struct path validates too (configs built in code)
	bad := Options{LX: &LXOptions{MTUAlign: &LXMTUAlign{Mode: "auto"}}}
	if _, err := ResolveLX(&bad); err == nil {
		t.Fatal("an unknown mode must fail resolve")
	}
}

// lx.quic_sniff_decoy (lx: SPEC 151): an object with four optional keys and
// their defaults (mode off, outbound_type [masque], detour_type [vless], send
// retry); the string and boolean forms are a load error; a default-only
// object canonicalizes to an absent key.
func TestLXQUICSniffDecoy(t *testing.T) {
	t.Parallel()
	off := (*LXResolved)(nil).QUICSniffDecoyOrOff()
	if off.Mode != LXQUICSniffDecoyOff || off.Enabled("masque") || !off.Retry {
		t.Fatalf("a nil *LXResolved must read as off with the defaults: %+v", off)
	}
	cases := []struct {
		content   string
		want      LXQUICSniffDecoyResolved
		canonical string // marshalled lx block, "" for absent
	}{
		{`{}`, LXQUICSniffDecoyResolved{Mode: "off", OutboundTypes: []string{"masque"}, DetourTypes: []string{"vless"}, Retry: true}, ""},
		{`{"lx":{"quic_sniff_decoy":{}}}`, LXQUICSniffDecoyResolved{Mode: "off", OutboundTypes: []string{"masque"}, DetourTypes: []string{"vless"}, Retry: true}, ""},
		{`{"lx":{"quic_sniff_decoy":{"mode":"off","send":"retry"}}}`, LXQUICSniffDecoyResolved{Mode: "off", OutboundTypes: []string{"masque"}, DetourTypes: []string{"vless"}, Retry: true}, ""},
		{`{"lx":{"quic_sniff_decoy":{"mode":"detour"}}}`, LXQUICSniffDecoyResolved{Mode: "detour", OutboundTypes: []string{"masque"}, DetourTypes: []string{"vless"}, Retry: true}, `{"quic_sniff_decoy":{"mode":"detour"}}`},
		{`{"lx":{"quic_sniff_decoy":{"mode":"all","outbound_type":["hysteria2","wireguard"],"detour_type":"socks","send":"always"}}}`, LXQUICSniffDecoyResolved{Mode: "all", OutboundTypes: []string{"hysteria2", "wireguard"}, DetourTypes: []string{"socks"}, Retry: false}, `{"quic_sniff_decoy":{"mode":"all","outbound_type":["hysteria2","wireguard"],"detour_type":"socks","send":"always"}}`},
		{`{"lx":{"quic_sniff_decoy":{"outbound_type":["tuic"]}}}`, LXQUICSniffDecoyResolved{Mode: "off", OutboundTypes: []string{"tuic"}, DetourTypes: []string{"vless"}, Retry: true}, `{"quic_sniff_decoy":{"outbound_type":"tuic"}}`},
	}
	for _, tc := range cases {
		options, resolved := mustResolveLX(t, tc.content)
		got := resolved.QUICSniffDecoyOrOff()
		if got.Mode != tc.want.Mode || got.Retry != tc.want.Retry || !slices.Equal(got.OutboundTypes, tc.want.OutboundTypes) || !slices.Equal(got.DetourTypes, tc.want.DetourTypes) {
			t.Fatalf("%s: got %+v, want %+v", tc.content, got, tc.want)
		}
		var canonical string
		if options.LX != nil {
			text, err := json.Marshal(options.LX)
			if err != nil {
				t.Fatal(err)
			}
			canonical = string(text)
		}
		if canonical != tc.canonical {
			t.Fatalf("%s: canonical %s, want %s", tc.content, canonical, tc.canonical)
		}
	}
	enabled := mustResolveLXResolved(t, `{"lx":{"quic_sniff_decoy":{"mode":"detour","outbound_type":["masque","tuic"]}}}`).QUICSniffDecoyOrOff()
	if !enabled.Enabled("masque") || !enabled.Enabled("tuic") || enabled.Enabled("hysteria2") || !enabled.DetourMatches("vless") || enabled.DetourMatches("socks") || enabled.DetourMatches("") {
		t.Fatalf("Enabled/DetourMatches: %+v", enabled)
	}
	rejected := []struct{ content, want string }{
		{`{"lx":{"quic_sniff_decoy":"detour"}}`, "must be an object"},
		{`{"lx":{"quic_sniff_decoy":true}}`, "must be an object"},
		{`{"lx":{"quic_sniff_decoy":false}}`, "must be an object"},
		{`{"lx":{"quic_sniff_decoy":1}}`, "must be an object"},
		{`{"lx":{"quic_sniff_decoy":{"mode":"on"}}}`, "lx.quic_sniff_decoy.mode"},
		{`{"lx":{"quic_sniff_decoy":{"send":"first"}}}`, "lx.quic_sniff_decoy.send"},
		{`{"lx":{"quic_sniff_decoy":{"outbound_type":["shadowsocks"]}}}`, "lx.quic_sniff_decoy.outbound_type"},
		{`{"lx":{"quic_sniff_decoy":{"detour_type":["selector"]}}}`, "is a group"},
		{`{"lx":{"quic_sniff_decoy":{"detour_type":[""]}}}`, "empty type"},
		{`{"lx":{"quic_sniff_decoy":{"protocols":["masque"]}}}`, "lx.quic_sniff_decoy"},
		{`{"lx":{"awg_masquerade":"direct"}}`, "lx.awg_masquerade is removed"},
		{`{"lx":{"awg_masquerade":true}}`, "lx.awg_masquerade is removed"},
	}
	for _, tc := range rejected {
		_, err := decodeLXConfig(t, tc.content)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want an error containing %q", tc.content, err, tc.want)
		}
	}
}
