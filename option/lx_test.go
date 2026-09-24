package option

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
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

func mustResolveLX(t *testing.T, content string) (Options, *LXResolved, []string) {
	t.Helper()
	options := mustDecodeLXConfig(t, content)
	resolved, warnings, err := ResolveLX(&options)
	if err != nil {
		t.Fatalf("resolve %s: %v", content, err)
	}
	return options, resolved, warnings
}

func TestLXBlockParsesEveryKey(t *testing.T) {
	t.Parallel()
	_, resolved, warnings := mustResolveLX(t, `{"lx": {
		"wg": {
			"idle_suspend": "30s",
			"idle_suspend_reachable": "5m",
			"idle_teardown": "10m",
			"lazy_build": true,
			"build_max": 4,
			"build_overflow": "build"
		},
		"masque": {"idle_timeout": "2m"}
	}}`)
	if len(warnings) != 0 {
		t.Fatalf("canonical keys must not warn, got %v", warnings)
	}
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
		MASQUE: LXMASQUEResolved{IdleTimeout: 2 * time.Minute},
	}
	if *resolved != want {
		t.Fatalf("resolved = %+v, want %+v", *resolved, want)
	}
}

func TestLXBlockEmptyEqualsAbsent(t *testing.T) {
	t.Parallel()
	for _, content := range []string{`{}`, `{"lx": {}}`, `{"lx": {"wg": {}, "masque": {}}}`} {
		options, resolved, warnings := mustResolveLX(t, content)
		if *resolved != (LXResolved{}) || len(warnings) != 0 {
			t.Fatalf("%s: expected all-off without warnings, got %+v %v", content, *resolved, warnings)
		}
		if options.LX != nil {
			t.Fatalf("%s: an empty block must canonicalize to absent, got %+v", content, *options.LX)
		}
	}
}

func TestLXResolvedNilIsAllOff(t *testing.T) {
	t.Parallel()
	var resolved *LXResolved
	if resolved.WGOrZero() != (LXWGResolved{}) || resolved.MASQUEOrZero() != (LXMASQUEResolved{}) {
		t.Fatal("a nil *LXResolved must read as all-off")
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
		`{"lx": {"foo": 1}}`,
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
		// the same rules apply to values that arrived through an alias
		{`{"route":{"lx_idle_suspend_reachable":"5m"}}`, "lx.wg.idle_suspend_reachable requires lx.wg.idle_suspend"},
		{`{"route":{"lx_idle_teardown":"0"}}`, "lx.wg.idle_teardown requires lx.wg.idle_suspend"},
	}
	for _, tc := range cases {
		options := mustDecodeLXConfig(t, tc.content)
		before := options
		_, _, err := ResolveLX(&options)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: expected %q, got %v", tc.content, tc.want, err)
		}
		if options.LX != before.LX || options.Route != before.Route {
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
	} {
		mustResolveLX(t, content)
	}
}

// idle_teardown: absent inherits idle_suspend_reachable, an explicit value
// wins, an explicit "0" disables teardown and survives decoding as a value.
func TestLXWGIdleTeardown(t *testing.T) {
	t.Parallel()
	_, inherited, _ := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m"}}}`)
	if inherited.WG.IdleTeardown != 5*time.Minute || inherited.WG.IdleTeardownSet {
		t.Fatalf("absent teardown must inherit the reachable window, got %+v", inherited.WG)
	}
	_, explicit, _ := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"1h"}}}`)
	if explicit.WG.IdleTeardown != time.Hour || !explicit.WG.IdleTeardownSet {
		t.Fatalf("explicit teardown must win, got %+v", explicit.WG)
	}
	options, disabled, _ := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s","idle_suspend_reachable":"5m","idle_teardown":"0"}}}`)
	if disabled.WG.IdleTeardown != 0 || !disabled.WG.IdleTeardownSet {
		t.Fatalf(`explicit "0" must disable teardown and stay a value, got %+v`, disabled.WG)
	}
	if options.LX.WG.IdleTeardown == nil || *options.LX.WG.IdleTeardown != 0 {
		t.Fatal(`explicit "0" must stay present in the canonical block`)
	}
	_, off, _ := mustResolveLX(t, `{"lx":{"wg":{"idle_suspend":"30s"}}}`)
	if off.WG.IdleTeardown != 0 || off.WG.IdleTeardownSet {
		t.Fatalf("no reachable window, no teardown key → teardown off, got %+v", off.WG)
	}
}

// The alias matrix, per key: only the old key (warning, value moves), only the
// new key (silent), both equal (warning), both different (error).
func TestLXAliases(t *testing.T) {
	t.Parallel()
	type aliasCase struct {
		legacy, current string // JSON fragments, "" = absent
		key             string
		check           func(LXWGResolved) bool
	}
	keys := []aliasCase{
		{`"lx_idle_suspend":"30s"`, `"idle_suspend":"30s"`, "idle_suspend",
			func(w LXWGResolved) bool { return w.IdleSuspend == 30*time.Second }},
		{`"lx_idle_suspend_reachable":"5m"`, `"idle_suspend_reachable":"5m"`, "idle_suspend_reachable",
			func(w LXWGResolved) bool { return w.IdleSuspendReachable == 5*time.Minute }},
		{`"lx_idle_teardown":"0"`, `"idle_teardown":"0"`, "idle_teardown",
			func(w LXWGResolved) bool { return w.IdleTeardown == 0 && w.IdleTeardownSet }},
	}
	// idle_suspend is the prerequisite of the other two; give it in lx.wg
	// when another key is under test.
	base := func(key string) string {
		if key == "idle_suspend" {
			return ""
		}
		return `"idle_suspend":"10s",`
	}
	for _, k := range keys {
		warning := "route.lx_" + k.key + " is deprecated, use lx.wg." + k.key

		// only the old key
		options, resolved, warnings := mustResolveLX(t,
			`{"route":{`+k.legacy+`},"lx":{"wg":{`+strings.TrimSuffix(base(k.key), ",")+`}}}`)
		if !k.check(resolved.WG) {
			t.Fatalf("%s: the alias value must move to lx.wg, got %+v", k.key, resolved.WG)
		}
		if len(warnings) != 1 || warnings[0] != warning {
			t.Fatalf("%s: expected %q, got %v", k.key, warning, warnings)
		}
		assertCanonical(t, k.key, options)

		// only the new key
		_, resolved, warnings = mustResolveLX(t, `{"lx":{"wg":{`+base(k.key)+k.current+`}}}`)
		if !k.check(resolved.WG) || len(warnings) != 0 {
			t.Fatalf("%s: new key alone must resolve silently, got %+v %v", k.key, resolved.WG, warnings)
		}

		// both, equal
		options, resolved, warnings = mustResolveLX(t,
			`{"route":{`+k.legacy+`},"lx":{"wg":{`+base(k.key)+k.current+`}}}`)
		if !k.check(resolved.WG) || len(warnings) != 1 || warnings[0] != warning {
			t.Fatalf("%s: equal values must resolve with the warning, got %+v %v", k.key, resolved.WG, warnings)
		}
		assertCanonical(t, k.key, options)

		// both, different
		different := strings.Replace(k.current, `"0"`, `"1h"`, 1)
		different = strings.Replace(different, `"30s"`, `"45s"`, 1)
		different = strings.Replace(different, `"5m"`, `"6m"`, 1)
		conflict := mustDecodeLXConfig(t, `{"route":{`+k.legacy+`},"lx":{"wg":{`+base(k.key)+different+`}}}`)
		_, _, err := ResolveLX(&conflict)
		want := "route.lx_" + k.key + " conflicts with lx.wg." + k.key
		if err == nil || err.Error() != want {
			t.Fatalf("%s: expected %q, got %v", k.key, want, err)
		}
	}
}

func assertCanonical(t *testing.T, key string, options Options) {
	t.Helper()
	if options.Route == nil {
		return
	}
	if options.Route.LXIdleSuspend != 0 || options.Route.LXIdleSuspendReachable != 0 || options.Route.LXIdleTeardown != nil {
		t.Fatalf("%s: route.lx_idle_* must be cleared after resolve, got %+v", key, *options.Route)
	}
	if options.LX == nil || options.LX.WG == nil {
		t.Fatalf("%s: lx.wg must carry the moved value", key)
	}
}

// Legacy-only and canonical input resolve to the same values, and the
// canonical form marshals the lx block without any route.lx_idle_* key.
func TestLXCanonicalForm(t *testing.T) {
	t.Parallel()
	legacy, legacyResolved, _ := mustResolveLX(t, `{"route":{
		"final": "direct",
		"lx_idle_suspend": "30s",
		"lx_idle_suspend_reachable": "5m",
		"lx_idle_teardown": "0"
	}}`)
	_, canonicalResolved, _ := mustResolveLX(t, `{"route":{"final":"direct"},"lx":{"wg":{
		"idle_suspend": "30s",
		"idle_suspend_reachable": "5m",
		"idle_teardown": "0"
	}}}`)
	if *legacyResolved != *canonicalResolved {
		t.Fatalf("legacy and lx input must resolve the same:\n  %+v\n  %+v", *legacyResolved, *canonicalResolved)
	}
	content, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "lx_idle_") {
		t.Fatalf("canonical form must not carry route.lx_idle_*: %s", text)
	}
	for _, fragment := range []string{`"lx":{"wg":{`, `"idle_suspend":"30s"`, `"idle_suspend_reachable":"5m0s"`, `"idle_teardown":"0s"`, `"final":"direct"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("canonical form lacks %s: %s", fragment, text)
		}
	}
	// the canonical form decodes back to the same values
	roundTrip, err := decodeLXConfig(t, text)
	if err != nil {
		t.Fatal(err)
	}
	again, warnings, err := ResolveLX(&roundTrip)
	if err != nil || len(warnings) != 0 || *again != *legacyResolved {
		t.Fatalf("canonical round trip: %+v %v %v", again, warnings, err)
	}
}

// Resolving twice is a no-op: same values, no warnings the second time, and
// the options are not rewritten.
func TestLXResolveIdempotent(t *testing.T) {
	t.Parallel()
	options, first, warnings := mustResolveLX(t, `{"route":{"lx_idle_suspend":"30s"},"lx":{"masque":{"idle_timeout":"5m"}}}`)
	if len(warnings) != 1 {
		t.Fatalf("expected one alias warning, got %v", warnings)
	}
	lx, route := options.LX, options.Route
	second, warnings, err := ResolveLX(&options)
	if err != nil || len(warnings) != 0 || *second != *first {
		t.Fatalf("second resolve: %+v %v %v", second, warnings, err)
	}
	if options.Route != route || *options.LX.WG != *lx.WG || *options.LX.MASQUE != *lx.MASQUE {
		t.Fatal("a second resolve must not change the canonical options")
	}
}

// ResolveLX replaces the LX and Route pointers instead of writing through them:
// resolving a shallow copy (the running-config snapshot does this) must leave
// the original options as they were.
func TestLXResolveDoesNotWriteThrough(t *testing.T) {
	t.Parallel()
	original := mustDecodeLXConfig(t, `{"route":{"lx_idle_suspend":"30s"},"lx":{"wg":{"build_max":2}}}`)
	snapshot := original
	if _, _, err := ResolveLX(&snapshot); err != nil {
		t.Fatal(err)
	}
	if original.Route.LXIdleSuspend != badoption.Duration(30*time.Second) {
		t.Fatal("resolving a copy cleared the original's route alias")
	}
	if original.LX.WG.IdleSuspend != 0 {
		t.Fatal("resolving a copy wrote into the original's lx block")
	}
	if snapshot.LX.WG.IdleSuspend != badoption.Duration(30*time.Second) || snapshot.Route.LXIdleSuspend != 0 {
		t.Fatalf("the copy must be canonical, got lx=%+v route=%+v", *snapshot.LX.WG, *snapshot.Route)
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
