//go:build with_lx_idle_suspend

// lx:begin idle-suspend
package route

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// lx: SPEC 097 — edge counting in the reachability walk.

func assertRefs(t *testing.T, got reachability, tag string, manual, auto int) {
	t.Helper()
	refs := got.refs[tag]
	if refs.manual != manual || refs.auto != auto {
		t.Errorf("%s: refs (manual %d, auto %d), want (%d, %d)", tag, refs.manual, refs.auto, manual, auto)
	}
}

// Every edge class in one tree: final → selector (manual root), the
// selector's choice (manual), a rule target urltest (manual root) and its
// pool (auto), a detour (auto) and a DNS-server detour (auto root).
func TestSelectionRefs_edgeClasses(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel"}, now: "wg-a", all: []string{"wg-a", "wg-b"}},
		&stubURLTest{stubOutbound: stubOutbound{tag: "auto"}, active: []string{"wg-a", "wg-c"}},
		&stubOutbound{tag: "vless", deps: []string{"wg-d"}},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
		&stubOutbound{tag: "wg-c"},
		&stubOutbound{tag: "wg-d"},
		&stubOutbound{tag: "wg-e"},
	)
	got := walkReachability([]string{"sel", "auto", "vless"}, []string{"wg-e"}, resolve)
	assertRefs(t, got, "sel", 1, 0)
	assertRefs(t, got, "auto", 1, 0)
	assertRefs(t, got, "vless", 1, 0)
	assertRefs(t, got, "wg-a", 1, 1)
	assertRefs(t, got, "wg-b", 0, 0)
	assertRefs(t, got, "wg-c", 0, 1)
	assertRefs(t, got, "wg-d", 0, 1)
	assertRefs(t, got, "wg-e", 0, 1)
	assertReachable(t, got.reachable, "sel", "auto", "vless", "wg-a", "wg-c", "wg-d", "wg-e")
}

// No inheritance along the tree: selector → urltest → X gives X one auto ref.
func TestSelectionRefs_noInheritanceThroughNestedGroup(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel"}, now: "auto", all: []string{"auto"}},
		&stubURLTest{stubOutbound: stubOutbound{tag: "auto"}, active: []string{"x"}},
		&stubOutbound{tag: "x"},
	)
	got := walkReachability([]string{"sel"}, nil, resolve)
	assertRefs(t, got, "sel", 1, 0)
	assertRefs(t, got, "auto", 1, 0)
	assertRefs(t, got, "x", 0, 1)
}

// Every incoming edge counts, but a tag is expanded once: two selectors on x
// give x two manual refs, and x's detour still gets one auto ref.
func TestSelectionRefs_edgesCountedTagExpandedOnce(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel-1"}, now: "x", all: []string{"x"}},
		&stubSelector{stubOutbound: stubOutbound{tag: "sel-2"}, now: "x", all: []string{"x"}},
		&stubOutbound{tag: "x", deps: []string{"y"}},
		&stubOutbound{tag: "y"},
	)
	got := walkReachability([]string{"sel-1", "sel-2"}, nil, resolve)
	assertRefs(t, got, "x", 2, 0)
	assertRefs(t, got, "y", 0, 1)
}

// A cycle counts its closing edge and terminates.
func TestSelectionRefs_cycle(t *testing.T) {
	resolve := resolverFrom(
		&stubOutbound{tag: "a", deps: []string{"b"}},
		&stubOutbound{tag: "b", deps: []string{"a"}},
	)
	got := walkReachability([]string{"a"}, nil, resolve)
	assertRefs(t, got, "a", 1, 1)
	assertRefs(t, got, "b", 0, 1)
}

// An unreachable group lends no refs: a selector nothing routes to does not
// count its choice.
func TestSelectionRefs_unreachableGroupCountsNothing(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "orphan"}, now: "x", all: []string{"x"}},
		&stubOutbound{tag: "direct"},
		&stubOutbound{tag: "x"},
	)
	got := walkReachability([]string{"direct"}, nil, resolve)
	assertRefs(t, got, "x", 0, 0)
	assertRefs(t, got, "orphan", 0, 0)
}

// reachableSet stays the plain reachable projection of the same walk.
func TestSelectionRefs_reachableSetUnchanged(t *testing.T) {
	resolve := resolverFrom(
		&stubSelector{stubOutbound: stubOutbound{tag: "sel"}, now: "wg-a", all: []string{"wg-a", "wg-b"}},
		&stubOutbound{tag: "wg-a"},
		&stubOutbound{tag: "wg-b"},
	)
	assertReachable(t, reachableSet([]string{"sel"}, resolve), "sel", "wg-a")
}

// SelectionRefs is served from the same cache as the reachable set and is
// computed with the idle tick off (lx.wg.build_max works without
// lx.wg.idle_suspend).
func TestSelectionRefs_router(t *testing.T) {
	mgr := &countingManager{}
	r := &Router{outbound: mgr} // idleSuspend == 0
	r.reachDirty.Store(true)
	if manual, auto := r.SelectionRefs("final"); manual != 1 || auto != 0 {
		t.Fatalf("final: (%d, %d), want (1, 0)", manual, auto)
	}
	r.SelectionRefs("final")
	r.reachableOutbounds()
	if mgr.defaultCalls != 1 {
		t.Fatalf("refs and reachable set share one cached walk, got %d walks", mgr.defaultCalls)
	}
	var reporter adapter.ReachabilityReporter = r
	if manual, auto := reporter.SelectionRefs("unknown"); manual != 0 || auto != 0 {
		t.Fatal("an unknown tag has no refs")
	}
}

// lx:end idle-suspend
