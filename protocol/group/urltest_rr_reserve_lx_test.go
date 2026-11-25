// lx:begin SPEC 019 R10 the reserve of the lazy run

package group

import (
	"context"
	"strconv"
	"testing"
)

// reserveGroup is a group of size live nodes with the pool of the first pool ones.
func reserveGroup(t *testing.T, size, pool int) (*healFixture, []string) {
	tags := make([]string, size)
	for i := range tags {
		tags[i] = "n" + strconv.Itoa(i)
	}
	return newHealFixture(t, tags, tags[:pool], 0), tags
}

func TestRRReserve_size(t *testing.T) {
	for pool, want := range map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 6: 3} {
		if got := rrPoolReserve(pool); got != want {
			t.Fatalf("pool %d: reserve %d, want %d", pool, got, want)
		}
	}
}

// R10: the lazy run stops at pool + reserve live nodes; the reserve takes no slot.
// A group of 30 live nodes, pool 3: 5 probes (3 before R10); pool 1: 2.
func TestRRReserve_stopsAtPoolPlusReserve(t *testing.T) {
	for pool, want := range map[int]int32{3: 5, 1: 2} {
		f, tags := reserveGroup(t, 30, pool)
		f.g.balancePool(context.Background())
		if got := f.allProbes(); got != want {
			t.Fatalf("pool %d: the run probed %d nodes, want %d", pool, got, want)
		}
		if !samePool(f.pool(), tags[:pool]...) {
			t.Fatalf("pool %d: the reserve takes no slot: %v", pool, f.pool())
		}
		t.Logf("group 30, all alive, pool %d: %d probes in the lazy run", pool, want)
	}
}

// R10: with fewer live nodes than pool + reserve the run walks to the end.
func TestRRReserve_fewLiveWalksToTheEnd(t *testing.T) {
	f, tags := reserveGroup(t, 10, 3)
	for _, tag := range tags[4:] {
		f.nodes[tag].dead.Store(true)
	}
	f.g.balancePool(context.Background())
	if got := f.allProbes(); got != 10 {
		t.Fatalf("4 live of 10: the run probes every node, got %d", got)
	}
}

// §8 item 19: the run probes a quarantined node even with the pool and the reserve
// full.
func TestRRReserve_quarantinedNodeProbed(t *testing.T) {
	f, tags := reserveGroup(t, 30, 3)
	f.s.balancer.rrQuarantine(tags[29])
	f.g.balancePool(context.Background())
	if f.probes(tags[29]) != 1 || f.allProbes() != 6 || f.s.balancer.isQuarantined(tags[29]) {
		t.Fatalf("the run lifts the quarantine and probes the node: %d probes of it, %d in all", f.probes(tags[29]), f.allProbes())
	}
}

// lx:end SPEC 019
