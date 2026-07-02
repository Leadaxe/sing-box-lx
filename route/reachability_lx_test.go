//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// stubOutbound is a minimal adapter.Outbound; only Tag/Dependencies are read by
// the walk (Type/Network/dial methods come from the embedded nil interface and
// are never called in these tests).
type stubOutbound struct {
	adapter.Outbound
	tag  string
	deps []string
}

func (o *stubOutbound) Tag() string            { return o.tag }
func (o *stubOutbound) Dependencies() []string { return o.deps }

// stubSelector is an OutboundGroup whose active choice is `now`.
type stubSelector struct {
	stubOutbound
	now string
	all []string
}

func (s *stubSelector) Now() string   { return s.now }
func (s *stubSelector) All() []string { return s.all }

// stubURLTest implements reachableActiveTags — its whole active pool is reachable.
type stubURLTest struct {
	stubOutbound
	active []string
}

func (u *stubURLTest) ActiveTags() []string { return u.active }
func (u *stubURLTest) Now() string {
	if len(u.active) > 0 {
		return u.active[0]
	}
	return ""
}
func (u *stubURLTest) All() []string { return u.active }

// resolverFrom builds a resolve func from a set of outbounds keyed by tag.
func resolverFrom(outbounds ...adapter.Outbound) func(string) (adapter.Outbound, bool) {
	m := make(map[string]adapter.Outbound)
	for _, o := range outbounds {
		m[o.Tag()] = o
	}
	return func(tag string) (adapter.Outbound, bool) {
		o, ok := m[tag]
		return o, ok
	}
}

func assertReachable(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
		if !got[w] {
			t.Errorf("expected %q reachable, but it is not (got %v)", w, keys(got))
		}
	}
	for tag := range got {
		if !wantSet[tag] {
			t.Errorf("unexpected reachable tag %q (got %v, want %v)", tag, keys(got), want)
		}
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestReachableFinalSeedOnly(t *testing.T) {
	// final = "wg-a"; nothing else.
	resolve := resolverFrom(&stubOutbound{tag: "wg-a"})
	got := reachableSet([]string{"wg-a"}, resolve)
	assertReachable(t, got, "wg-a")
}

func TestReachableDetourChain(t *testing.T) {
	// vless -> wg (detour). Seeding vless must reach wg.
	resolve := resolverFrom(
		&stubOutbound{tag: "vless", deps: []string{"wg"}},
		&stubOutbound{tag: "wg"},
	)
	got := reachableSet([]string{"vless"}, resolve)
	assertReachable(t, got, "vless", "wg")
}

func TestReachableSelectorOnlyNow(t *testing.T) {
	// selector picks "wg-a"; "wg-b" is a member but NOT selected → unreachable.
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel"}, now: "wg-a", all: []string{"wg-a", "wg-b"}},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
	)
	got := reachableSet([]string{"sel"}, resolve)
	assertReachable(t, got, "sel", "wg-a") // wg-b must be absent (suspend candidate)
}

func TestReachableURLTestWholePool(t *testing.T) {
	// urltest round_robin: the whole active pool is reachable, not just Now().
	resolve := resolverFrom(
		&stubURLTest{stubOutbound: stubOutbound{tag: "ut"}, active: []string{"wg-a", "wg-b", "wg-c"}},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
		&stubOutbound{tag: "wg-c"},
		&stubOutbound{tag: "wg-idle"}, // not in pool → must be unreachable
	)
	got := reachableSet([]string{"ut"}, resolve)
	assertReachable(t, got, "ut", "wg-a", "wg-b", "wg-c")
}

func TestReachableSelectorToSubSelector(t *testing.T) {
	// sel1 -> sel2 -> wg, following only active choices transitively.
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel1"}, now: "sel2"},
		&stubSelector{stubOutbound: stubOutbound{tag: "sel2"}, now: "wg"},
		&stubOutbound{tag: "wg"},
		&stubOutbound{tag: "wg-other"},
	)
	got := reachableSet([]string{"sel1"}, resolve)
	assertReachable(t, got, "sel1", "sel2", "wg")
}

func TestReachableCycleGuard(t *testing.T) {
	// a -> b -> a: the visited set must terminate the walk.
	resolve := resolverFrom(
		&stubOutbound{tag: "a", deps: []string{"b"}},
		&stubOutbound{tag: "b", deps: []string{"a"}},
	)
	got := reachableSet([]string{"a"}, resolve)
	assertReachable(t, got, "a", "b")
}

func TestReachableUnknownTag(t *testing.T) {
	// A seed that resolves to nothing is marked but expands nothing.
	resolve := resolverFrom(&stubOutbound{tag: "known"})
	got := reachableSet([]string{"missing"}, resolve)
	assertReachable(t, got, "missing")
}

func TestReachableEmptySelectorNow(t *testing.T) {
	// A cold selector with empty Now() expands to nothing (no panic, no "" tag).
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel"}, now: ""},
	)
	got := reachableSet([]string{"sel"}, resolve)
	assertReachable(t, got, "sel")
}

func TestReachableMultipleSeeds(t *testing.T) {
	// final + two rule targets, disjoint subtrees.
	resolve := resolverFrom(
		&stubOutbound{tag: "final"},
		&stubOutbound{tag: "ruleA", deps: []string{"wg-a"}},
		&stubOutbound{tag: "ruleB"},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-unrouted"}, // referenced by nobody
	)
	got := reachableSet([]string{"final", "ruleA", "ruleB"}, resolve)
	assertReachable(t, got, "final", "ruleA", "ruleB", "wg-a")
}

// TestReachableSelectorToURLTestPool mirrors the user's production topology
// (vpn-1 selector whose Now() is the nested vpn-1-auto urltest): a selector whose
// active choice is a round_robin urltest must descend into the urltest's WHOLE
// active pool, not just one node. A non-selected sibling member stays unreachable.
// Live-verified: switching vpn-1 → vpn-1-auto woke exactly the pool:4 members.
func TestReachableSelectorToURLTestPool(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "vpn-1"}, now: "vpn-auto", all: []string{"vpn-auto", "wg-x"}},
		&stubURLTest{stubOutbound: stubOutbound{tag: "vpn-auto"}, active: []string{"wg-a", "wg-b", "wg-c", "wg-d"}},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
		&stubOutbound{tag: "wg-c"},
		&stubOutbound{tag: "wg-d"},
		&stubOutbound{tag: "wg-x"},    // selector member, not Now → unreachable
		&stubOutbound{tag: "wg-idle"}, // nobody → unreachable
	)
	got := reachableSet([]string{"vpn-1"}, resolve)
	assertReachable(t, got, "vpn-1", "vpn-auto", "wg-a", "wg-b", "wg-c", "wg-d")
}

// TestReachableDualPathDedup covers a node reachable via two seeds at once — the
// user's WARP-1 is both vpn-1's Now() and vpn-3's default. The visited set must
// dedup (mark once, expand once) and the node must be reachable. Live-verified.
func TestReachableDualPathDedup(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "vpn-1"}, now: "warp-1", all: []string{"warp-1", "home"}},
		&stubSelector{stubOutbound: stubOutbound{tag: "vpn-3"}, now: "warp-1", all: []string{"warp-1", "warp-2"}},
		&stubOutbound{tag: "warp-1"},
		&stubOutbound{tag: "home"},   // vpn-1 member, not Now → unreachable
		&stubOutbound{tag: "warp-2"}, // vpn-3 member, not Now → unreachable
	)
	// Two seeds (final=vpn-1, rule→vpn-3) both reach warp-1.
	got := reachableSet([]string{"vpn-1", "vpn-3"}, resolve)
	assertReachable(t, got, "vpn-1", "vpn-3", "warp-1")
}

// TestReachableSelectorMemberIsURLTestNotSelected is the inverse of the pool case:
// when a urltest is a selector member that is NOT the active choice, its pool must
// stay unreachable (the whole nested subtree is dormant). This is the base
// production state: vpn-1 Now()=WARP-1 (an endpoint), so vpn-1-auto's pool sleeps.
func TestReachableSelectorMemberIsURLTestNotSelected(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "vpn-1"}, now: "warp-1", all: []string{"warp-1", "vpn-auto"}},
		&stubURLTest{stubOutbound: stubOutbound{tag: "vpn-auto"}, active: []string{"wg-a", "wg-b"}},
		&stubOutbound{tag: "warp-1"},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
	)
	got := reachableSet([]string{"vpn-1"}, resolve)
	// vpn-auto and its pool (wg-a, wg-b) must NOT appear — nested dormant subtree.
	assertReachable(t, got, "vpn-1", "warp-1")
}

// lx:end idle-suspend
