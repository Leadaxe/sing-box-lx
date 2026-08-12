package group

import (
	"context"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// balNode is a minimal adapter.Outbound exposing only Tag/Network — all pick() reads.
type balNode struct {
	adapter.Outbound
	tag string
}

func (n *balNode) Tag() string       { return n.tag }
func (n *balNode) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

// resolveFrom builds a pick() resolver over a fixed tag→node set.
func resolveFrom(tags ...string) ([]adapter.Outbound, func(string) adapter.Outbound) {
	nodes := map[string]adapter.Outbound{}
	for _, t := range tags {
		nodes[t] = &balNode{tag: t}
	}
	return nil, func(tag string) adapter.Outbound { return nodes[tag] }
}

func rrBalancer(t *testing.T, pool int, stickyHash []string) *balancer {
	t.Helper()
	opts := option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{Pool: pool, StickyHash: stickyHash},
	}
	b, err := newBalancer(opts)
	if err != nil {
		t.Fatalf("newBalancer: %v", err)
	}
	if b == nil {
		t.Fatal("round_robin balancer must not be nil")
	}
	return b
}

func destDomain(host string) M.Socksaddr { return M.Socksaddr{Fqdn: host} }

// --- newBalancer: modes, defaults, validation ---------------------------------------

func TestBalancerLeastTestIsNil(t *testing.T) {
	for _, mode := range []string{"", C.URLTestModeLeastTest} {
		b, err := newBalancer(option.URLTestOutboundOptions{Mode: mode})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if b != nil {
			t.Fatalf("mode %q must yield nil balancer (legacy path)", mode)
		}
	}
}

// warnLegacyTolerance: the startup hint fires only while the config still relies on the
// legacy `tolerance` knob — setting balancer.pool_tolerance silences it (issue #7).
func TestWarnLegacyTolerance(t *testing.T) {
	rr := func(bo *option.URLTestBalancerOptions, tolerance uint16) (b *balancer, opts option.URLTestOutboundOptions) {
		t.Helper()
		opts = option.URLTestOutboundOptions{Mode: C.URLTestModeRoundRobin, Balancer: bo}
		opts.Tolerance = tolerance
		var err error
		b, err = newBalancer(opts)
		if err != nil {
			t.Fatalf("newBalancer: %v", err)
		}
		return
	}
	// tolerance set, pool_tolerance absent → warn (user still thinks tolerance works).
	if b, opts := rr(nil, 50); !warnLegacyTolerance(b, opts) {
		t.Fatal("tolerance without pool_tolerance must warn")
	}
	// the issue #7 case: pool_tolerance set → the config adopted the round_robin knob, no warn.
	if b, opts := rr(&option.URLTestBalancerOptions{PoolTolerance: 150}, 50); warnLegacyTolerance(b, opts) {
		t.Fatal("pool_tolerance set: warning is noise, must not fire")
	}
	// no legacy tolerance at all → nothing to warn about.
	if b, opts := rr(&option.URLTestBalancerOptions{PoolTolerance: 150}, 0); warnLegacyTolerance(b, opts) {
		t.Fatal("no tolerance: must not warn")
	}
	// least_test (nil balancer): tolerance is honoured there, never warn.
	leastOpts := option.URLTestOutboundOptions{Mode: C.URLTestModeLeastTest}
	leastOpts.Tolerance = 50
	if warnLegacyTolerance(nil, leastOpts) {
		t.Fatal("least_test honours tolerance, must not warn")
	}
}

func TestBalancerUnknownModeRejected(t *testing.T) {
	if _, err := newBalancer(option.URLTestOutboundOptions{Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestBalancerDefaults(t *testing.T) {
	// round_robin without balancer → pool 3, stickiness on by [process,domain].
	b, err := newBalancer(option.URLTestOutboundOptions{Mode: C.URLTestModeRoundRobin})
	if err != nil {
		t.Fatal(err)
	}
	if b.poolSize != C.DefaultURLTestPool {
		t.Fatalf("default pool = %d, want %d", b.poolSize, C.DefaultURLTestPool)
	}
	if len(b.stickyHash) != 2 {
		t.Fatalf("default sticky_hash must be [process,domain], got %v", b.stickyHash)
	}
}

func TestBalancerStickyHashNoneDisables(t *testing.T) {
	// ["none"] is the explicit disable sentinel. A bare [] cannot be used (the config decoder
	// collapses it to nil, indistinguishable from omitted), so disabling goes through "none".
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{StickyHash: []string{C.URLTestStickyNone}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.stickyHash) != 0 {
		t.Fatalf("[\"none\"] must disable stickiness, got %v", b.stickyHash)
	}
}

func TestBalancerStickyHashEmptyIsDefault(t *testing.T) {
	// A bare [] (or nil) means default, NOT off — the decoder can't preserve an empty list, so
	// [] must fall back to the default rather than silently disabling stickiness.
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{StickyHash: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.stickyHash) != 2 {
		t.Fatalf("empty sticky_hash must default to [process,domain], got %v", b.stickyHash)
	}
}

func TestBalancerStickyHashNoneMixedRejected(t *testing.T) {
	// "none" mixed with real components is ambiguous → error.
	_, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{StickyHash: []string{C.URLTestStickyNone, C.URLTestStickyDomain}},
	})
	if err == nil {
		t.Fatal("\"none\" mixed with a real component must error")
	}
}

func TestBalancerNegativePoolRejected(t *testing.T) {
	_, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{Pool: -1},
	})
	if err == nil {
		t.Fatal("negative pool must error")
	}
}

func TestBalancerZeroPoolIsDefault(t *testing.T) {
	// pool:0 is indistinguishable from "omitted" for a Go int with omitempty, so it means
	// the default — NOT an error.
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{Pool: 0},
	})
	if err != nil {
		t.Fatalf("pool:0 must be treated as default, got error: %v", err)
	}
	if b.poolSize != C.DefaultURLTestPool {
		t.Fatalf("pool:0 → default %d, got %d", C.DefaultURLTestPool, b.poolSize)
	}
}

func TestBalancerPoolToleranceUpperBound(t *testing.T) {
	_, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{PoolTolerance: 15001},
	})
	if err == nil {
		t.Fatal("pool_tolerance above 15000 ms must be rejected at start")
	}
	if _, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{PoolTolerance: 15000},
	}); err != nil {
		t.Fatalf("pool_tolerance at the bound must be accepted, got %v", err)
	}
}

func TestBalancerUnknownStickyComponentRejected(t *testing.T) {
	_, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{StickyHash: []string{"bogus"}},
	})
	if err == nil {
		t.Fatal("unknown sticky_hash component must error")
	}
}

// --- pick: rotation, fallback ------------------------------------------------------

func TestRoundRobinRotation(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyNone}) // no stickiness → pure counter rotation
	b.setSlots([]string{"a", "b", "c"})
	_, resolve := resolveFrom("a", "b", "c")
	fb := &balNode{tag: "fb"}
	const rounds = 3000
	count := map[string]int{}
	for i := 0; i < rounds; i++ {
		count[b.pick(context.Background(), M.Socksaddr{}, fb, resolve).Tag()]++
	}
	for _, tag := range []string{"a", "b", "c"} {
		if count[tag] != rounds/3 {
			t.Errorf("node %s got %d, want %d", tag, count[tag], rounds/3)
		}
	}
}

func TestPickEmptyPoolFallback(t *testing.T) {
	b := rrBalancer(t, 3, []string{})
	_, resolve := resolveFrom("a")
	fb := &balNode{tag: "fb"}
	// no setSlots → empty pool → fallback.
	if got := b.pick(context.Background(), M.Socksaddr{}, fb, resolve); got.Tag() != "fb" {
		t.Fatalf("empty pool must return fallback, got %s", got.Tag())
	}
}

func TestPickUnresolvableTagFallsBack(t *testing.T) {
	b := rrBalancer(t, 1, []string{})
	b.setSlots([]string{"gone"})
	_, resolve := resolveFrom() // resolves nothing
	fb := &balNode{tag: "fb"}
	if got := b.pick(context.Background(), M.Socksaddr{}, fb, resolve); got.Tag() != "fb" {
		t.Fatalf("unresolvable slot tag must fall back, got %s", got.Tag())
	}
}

// --- sticky slot-hash: strict-zero reconnects ---------------------------------------

func TestStickySlotHashStable(t *testing.T) {
	b := rrBalancer(t, 4, []string{C.URLTestStickyDomain})
	b.setSlots([]string{"a", "b", "c", "d"})
	_, resolve := resolveFrom("a", "b", "c", "d")
	first := b.pick(context.Background(), destDomain("example.com"), nil, resolve).Tag()
	for i := 0; i < 100; i++ {
		got := b.pick(context.Background(), destDomain("example.com"), nil, resolve).Tag()
		if got != first {
			t.Fatalf("sticky key must map to one node: %s != %s", got, first)
		}
	}
}

func TestStickySlotHashLivingNodeKeepsKeysAcrossOtherSlotChanges(t *testing.T) {
	// The strict-zero-reconnect invariant: replacing the occupant of OTHER slots must not
	// move a key whose slot occupant is unchanged.
	b := rrBalancer(t, 4, []string{C.URLTestStickyDomain})
	b.setSlots([]string{"a", "b", "c", "d"})
	_, resolve := resolveFrom("a", "b", "c", "d", "x", "y")
	dst := destDomain("keep.me")
	pinnedTag := b.pick(context.Background(), dst, nil, resolve).Tag()
	pinnedSlot := int(hashKey("keep.me") % 4)

	// Replace every OTHER slot's occupant; the pinned slot keeps its tag.
	newSlots := []string{"a", "b", "c", "d"}
	for i := range newSlots {
		if i != pinnedSlot {
			newSlots[i] = []string{"x", "y", "x", "y"}[i]
		}
	}
	b.setSlots(newSlots)
	_, resolve2 := resolveFrom(append(newSlots, pinnedTag)...)
	if got := b.pick(context.Background(), dst, nil, resolve2).Tag(); got != pinnedTag {
		t.Fatalf("living node in its slot must keep its key: %s != %s", got, pinnedTag)
	}
}

func TestStickyEmptyKeyFixedSlot(t *testing.T) {
	// All components empty (no domain) → key "" → one fixed slot, no rotation.
	b := rrBalancer(t, 3, []string{C.URLTestStickyDomain})
	b.setSlots([]string{"a", "b", "c"})
	_, resolve := resolveFrom("a", "b", "c")
	first := b.pick(context.Background(), M.Socksaddr{}, nil, resolve).Tag()
	for i := 0; i < 50; i++ {
		if got := b.pick(context.Background(), M.Socksaddr{}, nil, resolve).Tag(); got != first {
			t.Fatalf("empty-key flows must not rotate: %s != %s", got, first)
		}
	}
}

// --- sticky key building ------------------------------------------------------------

func TestStickyKeyComponents(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyDestIP, C.URLTestStickyDestPort})
	dst := M.Socksaddr{Addr: netip.MustParseAddr("203.0.113.7"), Port: 443}
	if key := b.stickyKey(context.Background(), dst); key != "203.0.113.7\x00443" {
		t.Fatalf("unexpected key %q", key)
	}
	// Absent components collapse to "".
	b2 := rrBalancer(t, 3, []string{C.URLTestStickyDomain})
	if key := b2.stickyKey(context.Background(), M.Socksaddr{}); key != "" {
		t.Fatalf("absent domain must yield empty key, got %q", key)
	}
}

// Regression for the device-observed collapse-to-one-node bug: the router resolves a domain
// destination to an IP and overwrites metadata.Destination before the group dials, so
// destination.Fqdn is EMPTY at pick() time. The original domain survives in metadata.Domain.
// The sticky key must read metadata.Domain so domain traffic spreads across slots instead of
// every connection hashing to the same slot (process-only key). Verified on device: fixing this
// took chrome distribution from 28/1/1 (entropy 0.27) to spread across the pool.
func TestStickyKeyDomainFromMetadataWhenDestinationIsIP(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyProcess, C.URLTestStickyDomain})
	// destination is a resolved IP (Fqdn empty), the domain is only in metadata.Domain.
	resolved := M.Socksaddr{Addr: netip.MustParseAddr("142.250.1.1"), Port: 443}
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{
		Domain: "www.google.com",
	})
	key := b.stickyKey(ctx, resolved)
	// process empty (no ProcessInfo) + domain from metadata → "\x00www.google.com"
	if key != "\x00www.google.com" {
		t.Fatalf("domain must come from metadata.Domain when destination is an IP, got %q", key)
	}
	// Two different domains (same process) must yield different keys — the whole point.
	ctx2 := adapter.WithContext(context.Background(), &adapter.InboundContext{Domain: "www.reddit.com"})
	if k2 := b.stickyKey(ctx2, resolved); k2 == key {
		t.Fatal("different domains must produce different sticky keys (otherwise all traffic collapses to one slot)")
	}
}

func TestStickyKeyDomainFallsBackToFqdn(t *testing.T) {
	// When metadata.Domain is empty but destination still carries an Fqdn (e.g. direct dial,
	// no router resolve), fall back to destination.Fqdn.
	b := rrBalancer(t, 3, []string{C.URLTestStickyDomain})
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{}) // Domain empty
	if key := b.stickyKey(ctx, destDomain("example.com")); key != "example.com" {
		t.Fatalf("must fall back to destination.Fqdn, got %q", key)
	}
}

// --- planTolerantPool: top-N, replace-in-slot, fixed slots --------------------------

func cand(tag string, delay uint16) candidate {
	return candidate{tag: tag, delay: delay, alive: true}
}

func TestPlanTolerantPoolTopN(t *testing.T) {
	// Empty pool, 5 nodes, size 3 → 3 fastest by delay.
	results := map[string]candidate{
		"a": cand("a", 100), "b": cand("b", 20), "c": cand("c", 50),
		"d": cand("d", 10), "e": cand("e", 80),
	}
	next := planTolerantPool(nil, results, 3, 0)
	if len(next) != 3 {
		t.Fatalf("pool size = %d, want 3", len(next))
	}
	got := map[string]bool{}
	for _, tag := range next {
		got[tag] = true
	}
	// fastest three: d(10), b(20), c(50)
	for _, tag := range []string{"d", "b", "c"} {
		if !got[tag] {
			t.Errorf("top-3 must include %s, got %v", tag, next)
		}
	}
}

func TestPlanTolerantPoolKeepsLivingInSlot(t *testing.T) {
	// Current pool [a,b,c] all alive; a faster outsider exists but within tolerance → no churn.
	current := []string{"a", "b", "c"}
	results := map[string]candidate{
		"a": cand("a", 100), "b": cand("b", 100), "c": cand("c", 100),
		"x": cand("x", 90), // faster, but by only 10 — within tolerance 50
	}
	next := planTolerantPool(current, results, 3, 50)
	for i, tag := range []string{"a", "b", "c"} {
		if next[i] != tag {
			t.Fatalf("living node within tolerance must keep slot %d: got %v", i, next)
		}
	}
}

func TestPlanTolerantPoolEvictsBeyondTolerance(t *testing.T) {
	// Outsider beats the slot occupant by more than tolerance → it takes that slot.
	current := []string{"a", "b", "c"}
	results := map[string]candidate{
		"a": cand("a", 100), "b": cand("b", 100), "c": cand("c", 100),
		"x": cand("x", 10), // beats by 90 > tolerance 50
	}
	next := planTolerantPool(current, results, 3, 50)
	found := false
	for _, tag := range next {
		if tag == "x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outsider faster by > tolerance must enter the pool: %v", next)
	}
	if len(next) != 3 {
		t.Fatalf("pool must stay size 3, got %v", next)
	}
}

func TestPlanTolerantPoolDeadSlotReplaced(t *testing.T) {
	// b died; a live outsider must replace it (replace-in-slot, pool stays full).
	current := []string{"a", "b", "c"}
	results := map[string]candidate{
		"a": cand("a", 30), "c": cand("c", 30),
		"b": {tag: "b", alive: false},
		"x": cand("x", 40),
	}
	next := planTolerantPool(current, results, 3, 0)
	if len(next) != 3 {
		t.Fatalf("pool must stay full, got %v", next)
	}
	hasX, hasB := false, false
	for _, tag := range next {
		if tag == "x" {
			hasX = true
		}
		if tag == "b" {
			hasB = true
		}
	}
	if !hasX || hasB {
		t.Fatalf("dead b must be replaced by live x: %v", next)
	}
}

// Regression: a living slot occupant must NEVER change slot index when an outsider claims a
// DIFFERENT slot. Before the fix, eviction did delete(inPool, occupant), re-circulating the
// evicted-but-living node into a later slot — relocating it and moving its sticky keys.
// planTolerantPool(["a","b"], {a:100,b:200,c:10}, 2, 0) must yield ["c","b"], not ["c","a"]:
// c rightfully takes slot 0 (beats a), and the SURVIVOR b keeps slot 1 (a, evicted from slot 0,
// must not reappear at slot 1).
func TestPlanTolerantPoolSurvivorKeepsSlot(t *testing.T) {
	current := []string{"a", "b"}
	results := map[string]candidate{
		"a": cand("a", 100), "b": cand("b", 200), "c": cand("c", 10),
	}
	next := planTolerantPool(current, results, 2, 0)
	want := []string{"c", "b"}
	for i := range want {
		if next[i] != want[i] {
			t.Fatalf("survivor must keep its slot: got %v, want %v", next, want)
		}
	}
}

// Regression: one fast newcomer must not cascade-relocate multiple living survivors. With the
// old delete(inPool, occupant), a single fast node entering slot 0 could chain-bump occupants
// across every slot. Here only slot 0 should change; slots 1 and 2 keep their occupants.
func TestPlanTolerantPoolNoCascadeRelocation(t *testing.T) {
	current := []string{"a", "b", "c"}
	results := map[string]candidate{
		"a": cand("a", 100), "b": cand("b", 110), "c": cand("c", 120),
		"z": cand("z", 1), // one very fast outsider
	}
	next := planTolerantPool(current, results, 3, 0)
	// z takes slot 0 (beats a by > 0). b and c are each beaten by nobody NEW (z is used), so
	// they keep their original slots. a (evicted) must not resurface at slot 1 or 2.
	if next[0] != "z" {
		t.Fatalf("fastest outsider must take slot 0: %v", next)
	}
	if next[1] != "b" || next[2] != "c" {
		t.Fatalf("survivors b,c must keep their slots, no cascade: %v", next)
	}
}

// --- planFirstLivePool (pool_tolerance == 0): replace-in-slot, the FI-outlier guard ---------

// The core regression for the device-observed DE→FI→DE outlier. Pool [a,b,c]; the MIDDLE slot
// occupant b transiently fails its test (not live); a and c stay live; a live non-pool node d
// exists. Before the fix, balancePoolFirstLive compacted with append → c (slot 2) shifted to
// slot 1, moving every sticky key bound to slot 1/2 onto a different node. With replace-in-slot,
// c must KEEP slot 2 and d must fill the freed slot 1.
func TestPlanFirstLivePoolLiveNodeKeepsSlotAcrossDeadNeighbor(t *testing.T) {
	current := []string{"a", "b", "c"}
	live := map[string]bool{"a": true, "c": true, "d": true} // b is dead this round
	fillOrder := []string{"d"}                               // one live non-pool replacement
	next := planFirstLivePool(current, live, fillOrder, 3)
	want := []string{"a", "d", "c"}
	for i := range want {
		if next[i] != want[i] {
			t.Fatalf("dead middle slot must be replaced in place, neighbours fixed: got %v, want %v", next, want)
		}
	}
}

// A dead pool member with no live replacement keeps its own slot (pool never shrinks, and the
// dead node does not displace a living neighbour).
func TestPlanFirstLivePoolDeadMemberKeepsSlotWhenNoReplacement(t *testing.T) {
	current := []string{"a", "b", "c"}
	live := map[string]bool{"a": true, "c": true} // b dead, no live non-pool node
	next := planFirstLivePool(current, live, nil, 3)
	want := []string{"a", "b", "c"} // b stays in its slot
	for i := range want {
		if next[i] != want[i] {
			t.Fatalf("dead member must keep its slot when irreplaceable: got %v, want %v", next, want)
		}
	}
}

// Growing a short pool fills holes at the tail by index without disturbing existing members.
func TestPlanFirstLivePoolGrowsKeepingExisting(t *testing.T) {
	current := []string{"a"} // pool was size 1, now growing to 3
	live := map[string]bool{"a": true, "b": true, "c": true}
	next := planFirstLivePool(current, live, []string{"b", "c"}, 3)
	want := []string{"a", "b", "c"}
	for i := range want {
		if next[i] != want[i] {
			t.Fatalf("grow must keep existing slot 0 and fill tail: got %v, want %v", next, want)
		}
	}
}

// Mode() is what the Group.mode gRPC field carries. It must agree with the balancer
// discriminator Now()/Pool() already branch on, for every spelling of the option —
// including the empty default. SPEC 019 v2.
func TestURLTestModeReporting(t *testing.T) {
	for _, tc := range []struct {
		option string
		want   string
	}{
		{"", C.URLTestModeLeastTest},
		{C.URLTestModeLeastTest, C.URLTestModeLeastTest},
		{C.URLTestModeRoundRobin, C.URLTestModeRoundRobin},
	} {
		b, err := newBalancer(option.URLTestOutboundOptions{Mode: tc.option})
		if err != nil {
			t.Fatalf("mode %q: %v", tc.option, err)
		}
		s := &URLTest{balancer: b}
		if got := s.Mode(); got != tc.want {
			t.Fatalf("mode %q: Mode() = %q, want %q", tc.option, got, tc.want)
		}
	}
}
