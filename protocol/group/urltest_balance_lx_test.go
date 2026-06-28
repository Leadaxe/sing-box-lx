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

func TestBalancerStickyHashExplicitEmptyDisables(t *testing.T) {
	// Explicit [] → stickiness off. nil (omitted) → default. Distinguished by the slice.
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode:     C.URLTestModeRoundRobin,
		Balancer: &option.URLTestBalancerOptions{StickyHash: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.stickyHash) != 0 {
		t.Fatalf("explicit [] must disable stickiness, got %v", b.stickyHash)
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
	b := rrBalancer(t, 3, []string{}) // no stickiness → pure counter rotation
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
