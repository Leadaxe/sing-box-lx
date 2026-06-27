package group

import (
	"context"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// balNode is a minimal adapter.Outbound exposing only Tag/Network, which is all the
// balancer reads. The embedded nil interface panics on any other method — none are called.
type balNode struct {
	adapter.Outbound
	tag string
}

func (n *balNode) Tag() string       { return n.tag }
func (n *balNode) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func nodes(tags ...string) []adapter.Outbound {
	out := make([]adapter.Outbound, len(tags))
	for i, tag := range tags {
		out[i] = &balNode{tag: tag}
	}
	return out
}

func rrBalancer(t *testing.T) *balancer {
	t.Helper()
	b, err := newBalancer(option.URLTestOutboundOptions{Mode: C.URLTestModeRoundRobin})
	if err != nil {
		t.Fatalf("newBalancer: %v", err)
	}
	if b == nil {
		t.Fatal("round_robin balancer must not be nil")
	}
	return b
}

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

func TestBalancerLeastConnectionRejected(t *testing.T) {
	_, err := newBalancer(option.URLTestOutboundOptions{Mode: C.URLTestModeLeastConnection})
	if err == nil {
		t.Fatal("least_connection must be rejected in phase 1")
	}
}

func TestBalancerUnknownModeRejected(t *testing.T) {
	if _, err := newBalancer(option.URLTestOutboundOptions{Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestRoundRobinDistribution(t *testing.T) {
	b := rrBalancer(t)
	live := nodes("a", "b", "c")
	fallback := live[0]
	const rounds = 3000
	count := map[string]int{}
	for i := 0; i < rounds; i++ {
		count[b.pick(context.Background(), M.Socksaddr{}, live, fallback).Tag()]++
	}
	for _, tag := range []string{"a", "b", "c"} {
		// pure rotation over 3 nodes → exactly rounds/3 each.
		if count[tag] != rounds/len(live) {
			t.Errorf("node %s got %d, want %d", tag, count[tag], rounds/len(live))
		}
	}
}

func TestRoundRobinFallbackWhenNoneLive(t *testing.T) {
	b := rrBalancer(t)
	fallback := &balNode{tag: "fb"}
	got := b.pick(context.Background(), M.Socksaddr{}, nil, fallback)
	if got != adapter.Outbound(fallback) {
		t.Fatalf("empty live set must return fallback, got %v", got)
	}
}

func TestRoundRobinSingleLive(t *testing.T) {
	b := rrBalancer(t)
	live := nodes("only")
	for i := 0; i < 5; i++ {
		if got := b.pick(context.Background(), M.Socksaddr{}, live, live[0]).Tag(); got != "only" {
			t.Fatalf("single live node must always be picked, got %s", got)
		}
	}
}

// --- jump consistent hash -----------------------------------------------------------

func TestJumpConsistentHashBounds(t *testing.T) {
	for _, n := range []int{1, 2, 3, 8, 64} {
		for k := uint64(0); k < 1000; k++ {
			b := jumpConsistentHash(k*2654435761, n)
			if b < 0 || b >= n {
				t.Fatalf("jumphash out of range: key %d buckets %d -> %d", k, n, b)
			}
		}
	}
}

func TestJumpConsistentHashStable(t *testing.T) {
	// Same key + same bucket count must always map to the same bucket.
	for k := uint64(0); k < 100; k++ {
		first := jumpConsistentHash(k, 7)
		for i := 0; i < 10; i++ {
			if jumpConsistentHash(k, 7) != first {
				t.Fatalf("jumphash not deterministic for key %d", k)
			}
		}
	}
}

// --- sticky jumphash ----------------------------------------------------------------

func stickyBalancer(t *testing.T, stickyMode string, hash []string) *balancer {
	t.Helper()
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode:   C.URLTestModeRoundRobin,
		Sticky: &option.URLTestStickyOptions{Mode: stickyMode, Hash: hash},
	})
	if err != nil {
		t.Fatalf("newBalancer sticky: %v", err)
	}
	return b
}

func ctxWithDomain() context.Context { return context.Background() }

func destDomain(host string) M.Socksaddr { return M.Socksaddr{Fqdn: host} }

func TestStickyJumphashSameKeySameNode(t *testing.T) {
	b := stickyBalancer(t, C.URLTestStickyJumpHash, []string{C.URLTestStickyDomain})
	live := nodes("a", "b", "c", "d")
	first := b.pick(ctxWithDomain(), destDomain("example.com"), live, live[0]).Tag()
	for i := 0; i < 100; i++ {
		got := b.pick(ctxWithDomain(), destDomain("example.com"), live, live[0]).Tag()
		if got != first {
			t.Fatalf("sticky domain must always map to the same node: %s != %s", got, first)
		}
	}
	// A different domain may land elsewhere — just confirm it stays in-set.
	other := b.pick(ctxWithDomain(), destDomain("other.org"), live, live[0]).Tag()
	if findLive(other, live) == nil {
		t.Fatalf("picked node %s not in live set", other)
	}
}

func TestStickyEmptyKeyFixedNode(t *testing.T) {
	// All components empty (no domain on the destination) → key "" → one fixed node.
	b := stickyBalancer(t, C.URLTestStickyJumpHash, []string{C.URLTestStickyDomain})
	live := nodes("a", "b", "c")
	first := b.pick(context.Background(), M.Socksaddr{}, live, live[0]).Tag()
	for i := 0; i < 50; i++ {
		if got := b.pick(context.Background(), M.Socksaddr{}, live, live[0]).Tag(); got != first {
			t.Fatalf("empty-key flows must not rotate: %s != %s", got, first)
		}
	}
}

// --- sticky ttlmap ------------------------------------------------------------------

func TestStickyTTLMapStickAndExpire(t *testing.T) {
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode: C.URLTestModeRoundRobin,
		Sticky: &option.URLTestStickyOptions{
			Mode:    C.URLTestStickyTTLMap,
			Hash:    []string{C.URLTestStickyDomain},
			Timeout: 0, // → default 10m
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	live := nodes("a", "b", "c")
	dst := destDomain("sticky.test")
	first := b.pick(context.Background(), dst, live, live[0]).Tag()
	for i := 0; i < 20; i++ {
		if got := b.pick(context.Background(), dst, live, live[0]).Tag(); got != first {
			t.Fatalf("ttlmap key must stick: %s != %s", got, first)
		}
	}

	// Force expiry by aging the entry past the timeout, then a new pick must re-record.
	st := b.sticky
	st.access.Lock()
	for _, e := range st.entries {
		e.lastTime = time.Now().Add(-st.timeout - time.Hour)
	}
	st.access.Unlock()
	st.access.Lock()
	st.evictLocked(time.Now())
	n := len(st.entries)
	st.access.Unlock()
	if n != 0 {
		t.Fatalf("expired entries must be evicted, %d remain", n)
	}
}

func TestStickyTTLMapDeadNodeRepick(t *testing.T) {
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode: C.URLTestModeRoundRobin,
		Sticky: &option.URLTestStickyOptions{
			Mode: C.URLTestStickyTTLMap,
			Hash: []string{C.URLTestStickyDomain},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	dst := destDomain("repick.test")
	full := nodes("a", "b", "c")
	bound := b.pick(context.Background(), dst, full, full[0]).Tag()

	// Remove the bound node from the live set; the key must re-pin to a surviving node.
	var reduced []adapter.Outbound
	for _, n := range full {
		if n.Tag() != bound {
			reduced = append(reduced, n)
		}
	}
	got := b.pick(context.Background(), dst, reduced, reduced[0]).Tag()
	if got == bound {
		t.Fatalf("dead bound node %s must not be returned", bound)
	}
	if findLive(got, reduced) == nil {
		t.Fatalf("re-pinned node %s not in reduced live set", got)
	}
	// And the new binding must now be stable.
	for i := 0; i < 10; i++ {
		if again := b.pick(context.Background(), dst, reduced, reduced[0]).Tag(); again != got {
			t.Fatalf("re-pin not stable: %s != %s", again, got)
		}
	}
}

func TestStickyTTLMapCap(t *testing.T) {
	b, err := newBalancer(option.URLTestOutboundOptions{
		Mode: C.URLTestModeRoundRobin,
		Sticky: &option.URLTestStickyOptions{
			Mode: C.URLTestStickyTTLMap,
			Hash: []string{C.URLTestStickyDomain},
			Cap:  10,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	live := nodes("a", "b", "c")
	for i := 0; i < 100; i++ {
		b.pick(context.Background(), destDomain("host-"+strconv.Itoa(i)), live, live[0])
	}
	b.sticky.access.Lock()
	n := len(b.sticky.entries)
	b.sticky.access.Unlock()
	if n > 10 {
		t.Fatalf("ttlmap exceeded cap: %d > 10", n)
	}
}

// --- sticky key building ------------------------------------------------------------

func TestStickyKeyComponents(t *testing.T) {
	st, err := newStickyTable(&option.URLTestStickyOptions{
		Hash: []string{C.URLTestStickyDestIP, C.URLTestStickyDestPort},
	})
	if err != nil {
		t.Fatal(err)
	}
	dst := M.Socksaddr{Addr: netip.MustParseAddr("203.0.113.7"), Port: 443}
	key := st.key(context.Background(), dst)
	if key != "203.0.113.7\x00443" {
		t.Fatalf("unexpected key %q", key)
	}
	// Empty components collapse to "".
	stEmpty, _ := newStickyTable(&option.URLTestStickyOptions{Hash: []string{C.URLTestStickyDomain}})
	if k := stEmpty.key(context.Background(), M.Socksaddr{}); k != "" {
		t.Fatalf("absent domain must yield empty key, got %q", k)
	}
}

func TestStickyValidation(t *testing.T) {
	if _, err := newStickyTable(&option.URLTestStickyOptions{Mode: "bogus", Hash: []string{C.URLTestStickyDomain}}); err == nil {
		t.Fatal("unknown sticky mode must error")
	}
	if _, err := newStickyTable(&option.URLTestStickyOptions{Hash: []string{"bogus"}}); err == nil {
		t.Fatal("unknown hash component must error")
	}
	if _, err := newStickyTable(&option.URLTestStickyOptions{Hash: []string{C.URLTestStickyDomain}, Cap: -1}); err == nil {
		t.Fatal("negative cap must error")
	}
}
