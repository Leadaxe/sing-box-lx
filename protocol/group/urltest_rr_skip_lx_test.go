// lx:begin SPEC 138 round_robin dead slot skip tests

package group

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// aliveSet is a liveness predicate over a mutable set of live tags.
type aliveSet map[string]bool

func (s aliveSet) alive(tag string) bool { return s[tag] }

func liveTags(tags ...string) aliveSet {
	set := make(aliveSet, len(tags))
	for _, tag := range tags {
		set[tag] = true
	}
	return set
}

// stickyKeysBySlot returns domains whose sticky key lands in each slot of a pool of n.
func stickyKeysBySlot(t *testing.T, n int) [][]string {
	t.Helper()
	keys := make([][]string, n)
	for i := 0; i < 1000; i++ {
		domain := "host" + strconv.Itoa(i) + ".example"
		slot := int(hashKey(domain) % uint64(n))
		keys[slot] = append(keys[slot], domain)
	}
	for slot, slotKeys := range keys {
		if len(slotKeys) == 0 {
			t.Fatalf("no key lands in slot %d", slot)
		}
	}
	return keys
}

// Without stickiness the rotation goes over live slots only, evenly.
func TestRRSkip_counterRotatesOverLiveSlots(t *testing.T) {
	b := rrBalancer(t, 4, []string{C.URLTestStickyNone})
	b.setSlots([]string{"a", "b", "c", "d"})
	b.alive = liveTags("a", "c").alive
	_, resolve := resolveFrom("a", "b", "c", "d")
	fb := &balNode{tag: "fb"}
	count := map[string]int{}
	for i := 0; i < 4000; i++ {
		count[b.pick(context.Background(), M.Socksaddr{}, fb, resolve).Tag()]++
	}
	if count["a"] != 2000 || count["c"] != 2000 {
		t.Fatalf("want a=2000 c=2000 only, got %v", count)
	}
}

// A healthy pool rotates exactly as before the skip existed.
func TestRRSkip_counterAllLiveUnchanged(t *testing.T) {
	slots := []string{"a", "b", "c", "d"}
	legacy := rrBalancer(t, 4, []string{C.URLTestStickyNone})
	legacy.setSlots(slots)
	b := rrBalancer(t, 4, []string{C.URLTestStickyNone})
	b.setSlots(slots)
	b.alive = liveTags(slots...).alive
	_, resolve := resolveFrom(slots...)
	for i := 0; i < 100; i++ {
		want := legacy.pick(context.Background(), M.Socksaddr{}, nil, resolve).Tag()
		if got := b.pick(context.Background(), M.Socksaddr{}, nil, resolve).Tag(); got != want {
			t.Fatalf("pick %d: %s, want %s", i, got, want)
		}
	}
}

// Keys of live slots stay put. Keys of dead slots — two in a row here — spread
// evenly over the live slots, each key always to the same one. The slots
// themselves do not change.
func TestRRSkip_stickyDeadSlotKeysSpread(t *testing.T) {
	slots := []string{"a", "b", "c", "d"}
	b := rrBalancer(t, 4, []string{C.URLTestStickyDomain})
	b.setSlots(slots)
	b.alive = liveTags("a", "d").alive
	_, resolve := resolveFrom(slots...)
	moved := map[string]int{}
	var deadKeys int
	for slot, keys := range stickyKeysBySlot(t, 4) {
		for _, key := range keys {
			got := b.pick(context.Background(), destDomain(key), nil, resolve).Tag()
			for i := 0; i < 3; i++ {
				if again := b.pick(context.Background(), destDomain(key), nil, resolve).Tag(); again != got {
					t.Fatalf("key %s: %s then %s, must be deterministic", key, got, again)
				}
			}
			switch slots[slot] {
			case "a", "d":
				if got != slots[slot] {
					t.Fatalf("key %s of live slot %d moved to %s", key, slot, got)
				}
			default:
				if got != "a" && got != "d" {
					t.Fatalf("key %s of dead slot %d went to %s", key, slot, got)
				}
				moved[got]++
				deadKeys++
			}
		}
	}
	for _, tag := range []string{"a", "d"} {
		if share := float64(moved[tag]) / float64(deadKeys); share < 0.35 || share > 0.65 {
			t.Fatalf("dead-slot keys must spread evenly over live slots, got %v of %d", moved, deadKeys)
		}
	}
	if tags := b.poolTags(); len(tags) != 4 || tags[0] != "a" || tags[1] != "b" || tags[2] != "c" || tags[3] != "d" {
		t.Fatalf("pick must not change the slots, got %v", tags)
	}
}

// A node that comes back gets its keys back on the next connection; keys of
// live slots never move.
func TestRRSkip_stickyRecovery(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyDomain})
	b.setSlots([]string{"a", "b", "c"})
	live := liveTags("a", "c")
	b.alive = live.alive
	_, resolve := resolveFrom("a", "b", "c")
	keys := stickyKeysBySlot(t, 3)
	pick := func(key string) string {
		return b.pick(context.Background(), destDomain(key), nil, resolve).Tag()
	}
	moved := map[string]int{}
	for _, key := range keys[1] {
		moved[pick(key)]++
	}
	if moved["b"] != 0 || moved["a"] == 0 || moved["c"] == 0 {
		t.Fatalf("dead b: its keys must spread over a and c, got %v", moved)
	}
	live["b"] = true
	for _, key := range keys[1] {
		if got := pick(key); got != "b" {
			t.Fatalf("b alive again: key %s must return to b, got %s", key, got)
		}
	}
	for slot, tag := range map[int]string{0: "a", 2: "c"} {
		for _, key := range keys[slot] {
			if got := pick(key); got != tag {
				t.Fatalf("key %s of live slot %d moved to %s", key, slot, got)
			}
		}
	}
}

// No live slot (cold start, total outage): the pick is the one without the skip.
func TestRRSkip_allDeadKeepsLegacyPick(t *testing.T) {
	slots := []string{"a", "b", "c"}
	_, resolve := resolveFrom(slots...)
	for _, stickyHash := range [][]string{{C.URLTestStickyNone}, {C.URLTestStickyDomain}} {
		legacy := rrBalancer(t, 3, stickyHash)
		legacy.setSlots(slots)
		b := rrBalancer(t, 3, stickyHash)
		b.setSlots(slots)
		b.alive = liveTags().alive
		for i := 0; i < 30; i++ {
			dst := destDomain("host" + strconv.Itoa(i) + ".example")
			want := legacy.pick(context.Background(), dst, nil, resolve).Tag()
			if got := b.pick(context.Background(), dst, nil, resolve).Tag(); got != want {
				t.Fatalf("sticky %v pick %d: %s, want %s", stickyHash, i, got, want)
			}
		}
	}
}

func TestRRSkip_emptyPoolFallback(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyNone})
	b.alive = liveTags("a").alive
	_, resolve := resolveFrom("a")
	fb := &balNode{tag: "fb"}
	if got := b.pick(context.Background(), M.Socksaddr{}, fb, resolve); got.Tag() != "fb" {
		t.Fatalf("empty pool must return fallback, got %s", got.Tag())
	}
}

// The live slot chosen instead of a dead one still has to support the network.
func TestRRSkip_redirectedWrongNetworkFallsBack(t *testing.T) {
	b := rrBalancer(t, 3, []string{C.URLTestStickyNone})
	b.setSlots([]string{"a", "b", "c"})
	b.alive = liveTags("c").alive
	_, resolveAll := resolveFrom("a", "b", "c")
	resolve := func(tag string) adapter.Outbound {
		if tag == "c" {
			return nil // c does not support the connection's network
		}
		return resolveAll(tag)
	}
	fb := &balNode{tag: "fb"}
	for i := 0; i < 6; i++ {
		if got := b.pick(context.Background(), M.Socksaddr{}, fb, resolve).Tag(); got != "fb" {
			t.Fatalf("pick %d: %s, want fallback", i, got)
		}
	}
}

// Live path: the health-check keeps a dead member in its slot (no replacement),
// selectBalanced skips it, and history written or deleted from outside (a manual
// delay test) turns the skip off and on.
func TestRRSkip_deadMemberKeptButSkipped(t *testing.T) {
	a := &testDialNode{tag: "a", addr: "server"}
	b := &testDialNode{tag: "b"} // dead
	c := &testDialNode{tag: "c"} // dead, no replacement
	g := newPoolTestGroup(t, context.Background(), []*testDialNode{a, b, c}, 2)
	g.balancer.setSlots([]string{"a", "b"})
	s := &URLTest{outbound: g.outbound, group: g, balancer: g.balancer}
	s.balancer.alive = s.slotAlive

	g.balancePoolFirstLive(context.Background(), 2)
	if tags := g.balancer.poolTags(); tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("dead b must keep its slot, got %v", tags)
	}
	picks := func() map[string]int {
		count := map[string]int{}
		for i := 0; i < 10; i++ {
			count[s.selectBalanced(context.Background(), N.NetworkTCP, M.Socksaddr{}).Tag()]++
		}
		return count
	}
	if count := picks(); count["a"] != 10 {
		t.Fatalf("dead b must be skipped, got %v", count)
	}
	g.history.StoreURLTestHistory("b", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})
	if count := picks(); count["a"] != 5 || count["b"] != 5 {
		t.Fatalf("b alive again must rotate back, got %v", count)
	}
	g.history.DeleteURLTestHistory("b")
	if count := picks(); count["a"] != 10 {
		t.Fatalf("b deleted from history must be skipped again, got %v", count)
	}
}

// lx:end SPEC 138
