package option

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/schema"
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
			"idle": {
				"suspend": "30s",
				"suspend_reachable": "5m",
				"teardown": "10m",
				"lazy_build": true,
				"build_max": 4,
				"build_overflow": "build"
			},
			"buffers": {"read_datagram_count": 8, "send_by_mtu": false},
			"connected_socket": true
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
			ReadDatagramCount:    8,
			SendFullSize:         true,
			ConnectedSocket:      true,
		},
		MASQUE:      LXMASQUEResolved{IdleTimeout: 2 * time.Minute},
		TrustTunnel: LXTrustTunnelResolved{IdleTimeout: 3 * time.Minute},
		MTUAlign:    LXMTUAlignResolved{Mode: LXMTUAlignClamp},
		Detour:      LXDetourResolved{MTUAlign: LXMTUAlignClamp, Strip: lxDetourStripCatalog()},
	}
	if !reflect.DeepEqual(*resolved, want) {
		t.Fatalf("resolved = %+v, want %+v", *resolved, want)
	}
}

func TestLXBlockEmptyEqualsAbsent(t *testing.T) {
	t.Parallel()
	for _, content := range []string{`{}`, `{"lx": {}}`, `{"lx": {"wg": {}, "masque": {}, "trusttunnel": {}}}`,
		`{"lx": {"wg": {"idle": {}, "buffers": {}}}}`,
		`{"lx": {"wg": {"buffers": {"read_datagram_count": 128, "send_by_mtu": true}, "connected_socket": false}}}`,
	} {
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
// at its default (lx: SPEC 120), the WG buffer keys at theirs (SPEC 158 §9).
func lxDefaults() LXResolved {
	return LXResolved{
		WG:       LXWGResolved{ReadDatagramCount: 128},
		MTUAlign: LXMTUAlignResolved{Mode: LXMTUAlignClamp},
		Detour:   LXDetourResolved{MTUAlign: LXMTUAlignClamp, Strip: lxDetourStripCatalog()},
	}
}

func TestLXResolvedNilIsAllOff(t *testing.T) {
	t.Parallel()
	var resolved *LXResolved
	if resolved.WGOrZero() != (LXWGResolved{ReadDatagramCount: 128}) || resolved.MASQUEOrZero() != (LXMASQUEResolved{}) ||
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
		`{"lx": {"wg": {"idle": {"suspend_": "30s"}}}}`,
		`{"lx": {"wg": {"buffers": {"read_datagrams": 8}}}}`,
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
		{`{"lx":{"wg":{"idle":{"suspend":"-1s"}}}}`, "lx.wg.idle.suspend must be >= 0"},
		{`{"lx":{"wg":{"idle":{"suspend":"30s","suspend_reachable":"-1s"}}}}`, "lx.wg.idle.suspend_reachable must be >= 0"},
		{`{"lx":{"wg":{"idle":{"suspend":"30s","teardown":"-1s"}}}}`, "lx.wg.idle.teardown must be >= 0"},
		{`{"lx":{"wg":{"idle":{"suspend_reachable":"5m"}}}}`, "lx.wg.idle.suspend_reachable requires lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"teardown":"10m"}}}}`, "lx.wg.idle.teardown requires lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"teardown":"0"}}}}`, "lx.wg.idle.teardown requires lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"lazy_build":true}}}}`, "lx.wg.idle.lazy_build requires lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"suspend":"5m","suspend_reachable":"30s"}}}}`, "lx.wg.idle.suspend_reachable must be >= lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"build_max":-1}}}}`, "lx.wg.idle.build_max must be >= 0"},
		{`{"lx":{"wg":{"idle":{"build_overflow":"drop"}}}}`, `lx.wg.idle.build_overflow must be "wait" or "build"`},
		{`{"lx":{"wg":{"buffers":{"read_datagram_count":0}}}}`, "lx.wg.buffers.read_datagram_count must be between 1 and 128"},
		{`{"lx":{"wg":{"buffers":{"read_datagram_count":-1}}}}`, "lx.wg.buffers.read_datagram_count must be between 1 and 128"},
		{`{"lx":{"wg":{"buffers":{"read_datagram_count":129}}}}`, "lx.wg.buffers.read_datagram_count must be between 1 and 128"},
		{`{"lx":{"masque":{"idle_timeout":"-1s"}}}`, "lx.masque.idle_timeout must be >= 0"},
		{`{"lx":{"trusttunnel":{"idle_timeout":"-1s"}}}`, "lx.trusttunnel.idle_timeout must be >= 0"},
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
		`{"lx":{"wg":{"idle":{"build_max":0}}}}`,
		`{"lx":{"wg":{"idle":{"build_max":2,"build_overflow":"wait"}}}}`,
		`{"lx":{"wg":{"idle":{"suspend":"30s","suspend_reachable":"30s"}}}}`,
		`{"lx":{"wg":{"idle":{"suspend":"30s","teardown":"0","lazy_build":true}}}}`,
		`{"lx":{"masque":{"idle_timeout":"0"}}}`,
		`{"lx":{"trusttunnel":{"idle_timeout":"0"}}}`,
	} {
		mustResolveLX(t, content)
	}
}

// idle.teardown: absent inherits idle.suspend_reachable, an explicit value
// wins, an explicit "0" disables teardown and survives decoding as a value.
func TestLXWGIdleTeardown(t *testing.T) {
	t.Parallel()
	_, inherited := mustResolveLX(t, `{"lx":{"wg":{"idle":{"suspend":"30s","suspend_reachable":"5m"}}}}`)
	if inherited.WG.IdleTeardown != 5*time.Minute || inherited.WG.IdleTeardownSet {
		t.Fatalf("absent teardown must inherit the reachable window, got %+v", inherited.WG)
	}
	_, explicit := mustResolveLX(t, `{"lx":{"wg":{"idle":{"suspend":"30s","suspend_reachable":"5m","teardown":"1h"}}}}`)
	if explicit.WG.IdleTeardown != time.Hour || !explicit.WG.IdleTeardownSet {
		t.Fatalf("explicit teardown must win, got %+v", explicit.WG)
	}
	options, disabled := mustResolveLX(t, `{"lx":{"wg":{"idle":{"suspend":"30s","suspend_reachable":"5m","teardown":"0"}}}}`)
	if disabled.WG.IdleTeardown != 0 || !disabled.WG.IdleTeardownSet {
		t.Fatalf(`explicit "0" must disable teardown and stay a value, got %+v`, disabled.WG)
	}
	if options.LX.WG.Idle.Teardown == nil || *options.LX.WG.Idle.Teardown != 0 {
		t.Fatal(`explicit "0" must stay present in the canonical block`)
	}
	_, off := mustResolveLX(t, `{"lx":{"wg":{"idle":{"suspend":"30s"}}}}`)
	if off.WG.IdleTeardown != 0 || off.WG.IdleTeardownSet {
		t.Fatalf("no reachable window, no teardown key → teardown off, got %+v", off.WG)
	}
}

// The canonical form marshals and decodes back to the same values.
func TestLXCanonicalForm(t *testing.T) {
	t.Parallel()
	options, resolved := mustResolveLX(t, `{"route":{"final":"direct"},"lx":{"wg":{
		"idle": {"suspend": "30s", "suspend_reachable": "5m", "teardown": "0"},
		"buffers": {"read_datagram_count": 8, "send_by_mtu": false},
		"connected_socket": true
	}}}`)
	content, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, fragment := range []string{
		`"lx":{"wg":{"idle":{`, `"suspend":"30s"`, `"suspend_reachable":"5m0s"`, `"teardown":"0s"`, `"final":"direct"`,
		`"buffers":{"read_datagram_count":8,"send_by_mtu":false}`, `"connected_socket":true`,
	} {
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
	options, first := mustResolveLX(t, `{"lx":{"wg":{"idle":{"suspend":"30s"},"buffers":{"read_datagram_count":8}},"masque":{"idle_timeout":"5m"}}}`)
	lx := options.LX
	second, err := ResolveLX(&options)
	if err != nil || !reflect.DeepEqual(*second, *first) {
		t.Fatalf("second resolve: %+v %v", second, err)
	}
	if *options.LX.WG.Idle != *lx.WG.Idle || *options.LX.WG.Buffers != *lx.WG.Buffers || *options.LX.MASQUE != *lx.MASQUE {
		t.Fatal("a second resolve must not change the canonical options")
	}
}

// ResolveLX replaces the LX pointer instead of writing through it: resolving a
// shallow copy (the running-config snapshot does this) must leave the original
// options as they were.
func TestLXResolveDoesNotWriteThrough(t *testing.T) {
	t.Parallel()
	original := mustDecodeLXConfig(t, `{"lx":{"wg":{"idle":{"build_max":2},"buffers":{"read_datagram_count":128}},"trusttunnel":{}}}`)
	snapshot := original
	if _, err := ResolveLX(&snapshot); err != nil {
		t.Fatal(err)
	}
	if original.LX.TrustTunnel == nil || original.LX.WG.Buffers == nil || original.LX.WG.Buffers.ReadDatagramCount == nil {
		t.Fatal("resolving a copy wrote into the original's lx block")
	}
	if snapshot.LX.TrustTunnel != nil || snapshot.LX.WG.Idle.BuildMax != 2 || snapshot.LX.WG.Buffers != nil {
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

// lx: SPEC 165 — lx.detour (ALGORITHM Р1–Р9).
func TestLXDetourР1toР7(t *testing.T) {
	t.Parallel()
	catalog := lxDetourStripCatalog()
	none := LXDetourStripResolved{}
	cases := []struct {
		name    string
		content string
		want    LXDetourResolved
	}{
		{"Р1 absent", `{}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р2 true", `{"lx":{"detour":true}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р3 false", `{"lx":{"detour":false}}`, LXDetourResolved{MTUAlign: "off", Strip: none}},
		{"Р4 empty object", `{"lx":{"detour":{}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р4 mode", `{"lx":{"detour":{"mtu_align":"fill"}}}`, LXDetourResolved{MTUAlign: "fill", Strip: catalog}},
		{"Р5 strip false", `{"lx":{"detour":{"strip":false}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: none}},
		{"Р5 strip true", `{"lx":{"detour":{"strip":true}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р5 strip without filters", `{"lx":{"detour":{"strip":{}}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р6 filters false", `{"lx":{"detour":{"strip":{"filters":false}}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: none}},
		{"Р6 filters true", `{"lx":{"detour":{"strip":{"filters":true}}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog}},
		{"Р6 Т8 empty filters", `{"lx":{"detour":{"strip":{"filters":{}}}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: none}},
		{"Р6 override", `{"lx":{"detour":{"strip":{"filters":{"tls":{"fragment":true,"engine":true}}}}}}`,
			LXDetourResolved{MTUAlign: "clamp", Strip: LXDetourStripResolved{TLSFragment: true, TLSEngine: true}}},
		{"except", `{"lx":{"detour":{"except_outbounds":["a","b"]}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog, ExceptOutbounds: []string{"a", "b"}}},
		{"except string", `{"lx":{"detour":{"except_outbounds":"a"}}}`, LXDetourResolved{MTUAlign: "clamp", Strip: catalog, ExceptOutbounds: []string{"a"}}},
	}
	for _, tc := range cases {
		options, resolved := mustResolveLX(t, tc.content)
		if !reflect.DeepEqual(resolved.Detour, tc.want) {
			t.Fatalf("%s: %+v, want %+v", tc.name, resolved.Detour, tc.want)
		}
		if want := (LXMTUAlignResolved{Mode: tc.want.MTUAlign, Except: tc.want.ExceptOutbounds}); !reflect.DeepEqual(resolved.MTUAlignOrDefault(), want) {
			t.Fatalf("%s: mtu_align %+v", tc.name, resolved.MTUAlignOrDefault())
		}
		// Н1: the running config keeps the block as written and resolves the same.
		content, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ResolveLX(common.Ptr(mustDecodeLXConfig(t, string(content))))
		if err != nil || !reflect.DeepEqual(again.Detour, tc.want) {
			t.Fatalf("%s: round trip %s → %+v %v", tc.name, content, again, err)
		}
		// Р9: the expanded form resolves the same and names every key.
		expanded, err := json.Marshal(ExpandedLXDetour(tc.want))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"mtu_align", "except_outbounds", "fragment", "spoof", "kernel", "engine"} {
			if !strings.Contains(string(expanded), `"`+key+`"`) {
				t.Fatalf("%s: Р9 expanded form %s misses %s", tc.name, expanded, key)
			}
		}
		var back LXDetour
		if err := json.Unmarshal(expanded, &back); err != nil || !reflect.DeepEqual(back.Resolve(), tc.want) && len(tc.want.ExceptOutbounds) > 0 {
			t.Fatalf("%s: expanded %s → %+v %v", tc.name, expanded, back.Resolve(), err)
		}
	}
}

func TestLXDetourР8Errors(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]string{
		`{"lx":{"mtu_align":"clamp"}}`:                                           "moved to lx.detour.mtu_align",
		`{"lx":{"mtu_align":null}}`:                                              "moved to lx.detour.mtu_align",
		`{"lx":{"detour":{"bogus":1}}}`:                                          "lx.detour: unknown key bogus",
		`{"lx":{"detour":{"mtu_align":"auto"}}}`:                                 "lx.detour.mtu_align",
		`{"lx":{"detour":{"strip":{"x":1}}}}`:                                    "lx.detour.strip: unknown key x",
		`{"lx":{"detour":{"strip":{"filters":{"udp":{}}}}}}`:                     "lx.detour.strip.filters: unknown key udp",
		`{"lx":{"detour":{"strip":{"filters":{"tls":{"utls":true}}}}}}`:          "lx.detour.strip.filters.tls: unknown key utls",
		`{"lx":{"detour":{"strip":{"filters":{"multiplex":{"padding":true}}}}}}`: "unknown key multiplex",
		`{"lx":{"detour":{"strip":{"filters":{"tls":{"fragment":1}}}}}}`:         "lx.detour.strip.filters.tls.fragment must be true or false",
		`{"lx":{"detour":{"except_outbounds":[""]}}}`:                            "lx.detour.except_outbounds: empty tag",
		`{"lx":{"detour":1}}`:                                                    "lx.detour must be true, false or an object",
	} {
		if _, err := decodeLXConfig(t, content); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", content, want, err)
		}
	}
}

// lx.quic_sniff_decoy is removed (lx: SPEC 060) and lx.awg_masquerade before
// it (SPEC 153): any value of either is a load error that names the reason.
func TestLXRemovedKeys(t *testing.T) {
	t.Parallel()
	rejected := []struct{ content, want string }{
		{`{"lx":{"quic_sniff_decoy":{"mode":"detour"}}}`, "lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic"},
		{`{"lx":{"quic_sniff_decoy":{}}}`, "lx.quic_sniff_decoy is removed"},
		{`{"lx":{"quic_sniff_decoy":"detour"}}`, "lx.quic_sniff_decoy is removed"},
		{`{"lx":{"quic_sniff_decoy":true}}`, "lx.quic_sniff_decoy is removed"},
		{`{"lx":{"quic_sniff_decoy":null}}`, "lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic"},
		{`{"lx":{"sniff_guard":{"quic":{"method":"decoy_first_byte"}},"quic_sniff_decoy":null}}`, "lx.quic_sniff_decoy is removed"},
		{`{"lx":{"awg_masquerade":null}}`, "lx.awg_masquerade is removed"},
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

// lx: SPEC 098 §3 — the flat idle keys moved into lx.wg.idle with no
// compatibility period: each one, null included, fails the load and names
// the new key.
func TestLXWGMovedIdleKeys(t *testing.T) {
	t.Parallel()
	cases := []struct{ content, want string }{
		{`{"lx":{"wg":{"idle_suspend":"30s"}}}`, "lx.wg.idle_suspend is moved to lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle_suspend_reachable":"5m"}}}`, "lx.wg.idle_suspend_reachable is moved to lx.wg.idle.suspend_reachable"},
		{`{"lx":{"wg":{"idle_teardown":"0"}}}`, "lx.wg.idle_teardown is moved to lx.wg.idle.teardown"},
		{`{"lx":{"wg":{"lazy_build":true}}}`, "lx.wg.lazy_build is moved to lx.wg.idle.lazy_build"},
		{`{"lx":{"wg":{"build_max":2}}}`, "lx.wg.build_max is moved to lx.wg.idle.build_max"},
		{`{"lx":{"wg":{"build_overflow":"wait"}}}`, "lx.wg.build_overflow is moved to lx.wg.idle.build_overflow"},
		{`{"lx":{"wg":{"idle_suspend":null}}}`, "lx.wg.idle_suspend is moved to lx.wg.idle.suspend"},
		{`{"lx":{"wg":{"idle":{"suspend":"30s"},"lazy_build":true}}}`, "lx.wg.lazy_build is moved to lx.wg.idle.lazy_build"},
	}
	for _, tc := range cases {
		_, err := decodeLXConfig(t, tc.content)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want an error containing %q", tc.content, err, tc.want)
		}
	}
}

// Each idle key resolves to what its flat predecessor resolved to; the
// resolved field names did not move.
func TestLXWGIdleResolvesAsFlat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		content string
		want    func(*LXWGResolved)
	}{
		{`{"suspend":"30s"}`, func(r *LXWGResolved) { r.IdleSuspend = 30 * time.Second }},
		{`{"suspend":"30s","suspend_reachable":"5m"}`, func(r *LXWGResolved) {
			r.IdleSuspend, r.IdleSuspendReachable, r.IdleTeardown = 30*time.Second, 5*time.Minute, 5*time.Minute
		}},
		{`{"suspend":"30s","teardown":"10m"}`, func(r *LXWGResolved) {
			r.IdleSuspend, r.IdleTeardown, r.IdleTeardownSet = 30*time.Second, 10*time.Minute, true
		}},
		{`{"suspend":"30s","lazy_build":true}`, func(r *LXWGResolved) { r.IdleSuspend, r.LazyBuild = 30*time.Second, true }},
		{`{"build_max":4}`, func(r *LXWGResolved) { r.BuildMax = 4 }},
		{`{"build_overflow":"build"}`, func(r *LXWGResolved) { r.BuildOverflow = LXBuildOverflowBuild }},
	}
	for _, tc := range cases {
		want := lxDefaults().WG
		tc.want(&want)
		resolved := mustResolveLXResolved(t, `{"lx":{"wg":{"idle":`+tc.content+`}}}`)
		if resolved.WG != want {
			t.Fatalf("%s: %+v, want %+v", tc.content, resolved.WG, want)
		}
	}
}

// lx: SPEC 158 §9.1 — lx.wg.buffers: 1–128, the defaults written out
// canonicalize away, send_by_mtu false is kept.
func TestLXWGBuffers(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 8, 128} {
		options, resolved := mustResolveLX(t, `{"lx":{"wg":{"buffers":{"read_datagram_count":`+strconv.Itoa(count)+`}}}}`)
		if resolved.WG.ReadDatagramCount != count || resolved.WG.SendFullSize {
			t.Fatalf("read_datagram_count %d: %+v", count, resolved.WG)
		}
		if count == 128 {
			if options.LX != nil {
				t.Fatalf("read_datagram_count 128 must canonicalize to absent, got %+v", *options.LX.WG)
			}
		} else if options.LX.WG.Buffers.ReadDatagramCount == nil || *options.LX.WG.Buffers.ReadDatagramCount != count {
			t.Fatalf("read_datagram_count %d must stay in the canonical block", count)
		}
	}
	options, resolved := mustResolveLX(t, `{"lx":{"wg":{"buffers":{"send_by_mtu":true}}}}`)
	if resolved.WG.SendFullSize || options.LX != nil {
		t.Fatal("send_by_mtu true is the default and must canonicalize to absent")
	}
	options, resolved = mustResolveLX(t, `{"lx":{"wg":{"buffers":{"send_by_mtu":false}}}}`)
	if !resolved.WG.SendFullSize || resolved.WG.ReadDatagramCount != 128 {
		t.Fatalf("send_by_mtu false: %+v", resolved.WG)
	}
	if options.LX.WG.Buffers.SendByMTU == nil || *options.LX.WG.Buffers.SendByMTU {
		t.Fatal("send_by_mtu false must stay in the canonical block")
	}
}

// lx: SPEC 160 §4 — lx.wg.connected_socket: absent = false, false
// canonicalizes away.
func TestLXWGConnectedSocket(t *testing.T) {
	t.Parallel()
	options, resolved := mustResolveLX(t, `{"lx":{"wg":{"connected_socket":true}}}`)
	if !resolved.WG.ConnectedSocket || !options.LX.WG.ConnectedSocket {
		t.Fatal("connected_socket true must resolve and stay in the canonical block")
	}
	options, resolved = mustResolveLX(t, `{"lx":{"wg":{"connected_socket":false}}}`)
	if resolved.WG.ConnectedSocket || options.LX != nil {
		t.Fatal("connected_socket false is the default and must canonicalize to absent")
	}
}

func TestLXWGSchema(t *testing.T) {
	t.Parallel()
	content, err := schema.Generate(context.Background(), reflect.TypeFor[LXOptions]())
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, fragment := range []string{`"LXWGOptions"`, `"LXWGIdleOptions"`, `"LXWGBuffersOptions"`, `"suspend_reachable"`, `"read_datagram_count"`, `"send_by_mtu"`, `"connected_socket"`, `"deprecated": true`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("schema lacks %s", fragment)
		}
	}
}
