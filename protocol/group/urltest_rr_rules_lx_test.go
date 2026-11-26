// lx:begin SPEC 138 / SPEC 019 round_robin: rules with no test of their own before
// (the seed of the pool, the scheduled run with pool_tolerance > 0, the manual run,
// W5, the wake event of N3, S3)

package group

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

// SPEC 019: before the first run the pool takes the nodes with history, fastest
// first, then the rest in config order.
func TestRRRun_seedPool(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	for _, tag := range []string{"a", "b", "c", "d", "e"} {
		f.g.history.DeleteURLTestHistory(tag)
	}
	f.g.history.StoreURLTestHistory("c", &adapter.URLTestHistory{Time: time.Now(), Delay: 50})
	f.g.history.StoreURLTestHistory("e", &adapter.URLTestHistory{Time: time.Now(), Delay: 10})
	f.g.balancer.setSlots(nil)
	f.g.seedPool()
	if !samePool(f.pool(), "e", "c", "a") {
		t.Fatalf("seed: %v, want [e c a]", f.pool())
	}
	least := &URLTestGroup{outbounds: f.g.outbounds, history: f.g.history}
	least.seedPool() // least_test: no pool, nothing to seed
}

// SPEC 019 pool_tolerance > 0: the scheduled run tests every node; a dead slot takes
// the fastest live node outside the pool, a live node keeps its slot unless a node
// outside is faster by more than the tolerance, and then that node takes its slot.
func TestRRRun_tolerantPool(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 50)
	f.nodes["a"].slow = 150 * time.Millisecond
	f.nodes["c"].dead.Store(true)
	result := f.g.balancePool(context.Background())
	if _, tested := result["e"]; !tested || len(result) != 4 {
		t.Fatalf("the run tests every node: %v", result)
	}
	pool := f.pool()
	if len(pool) != 3 || pool[1] != "b" {
		t.Fatalf("b is live and nobody beats it by 50 ms: %v", pool)
	}
	for _, slot := range []int{0, 2} {
		if pool[slot] != "d" && pool[slot] != "e" {
			t.Fatalf("slot %d: the slow a and the dead c give way to d and e: %v", slot, pool)
		}
	}
}

// The manual run (URLTest: every node) lifts the K5 marks and the quarantine, lays
// the pool out with the live nodes in their slots (a dead one gives its slot to a
// live node outside), and its answers bring the pause back to the base (W3).
func TestRRRun_manualRun(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	b := f.s.balancer
	b.rrQuarantine("d")
	table := &b.memory.shifts
	table.access.Lock()
	table.entry(42).marked = true
	table.access.Unlock()
	f.nodes["b"].dead.Store(true)
	end := time.Now()
	b.heal.setPauseStep(2)
	b.heal.setPauseEnd(&end)
	if _, err := f.g.URLTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.isQuarantined("d") || table.marked(42) {
		t.Fatal("the manual run lifts the quarantine and the K5 marks")
	}
	if !samePool(f.pool(), "a", "d", "c") {
		t.Fatalf("the live nodes keep their slots, d takes the slot of the dead b: %v", f.pool())
	}
	if b.heal.pauseStep() != 0 {
		t.Fatalf("an answer of the manual run brings the step back to the base: %d", b.heal.pauseStep())
	}
}

// W5: the round of the right xr is the check while a suspicion asks for it (D4).
func TestRRPause_extraRoundChecks(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	end := time.Now()
	heal.setPauseStep(1)
	heal.setPauseEnd(&end)
	if !heal.grantExtra(time.Now(), time.Now()) {
		t.Fatal("no right xr in a pause above the base")
	}
	now := rrClock(time.Now())
	track := f.s.balancer.trackOf("a")
	track.suspectAt(now, now)
	track.wantCheck.Store(true)
	f.s.rrExtraRound()
	waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	if f.probes("a") == 0 || f.probes("b") == 0 || f.probes("c") == 0 {
		t.Fatal("the round of the right is the check of the pool")
	}
	if heal.extraOpen() {
		t.Fatal("the right is spent by its round")
	}
}

// W5: with no suspicion and an empty slot the round of the right is the search.
func TestRRPause_extraRoundSearches(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	f.s.balancer.clearSlot("a")
	end := time.Now()
	heal.setPauseStep(1)
	heal.setPauseEnd(&end)
	heal.grantExtra(time.Now(), time.Now())
	f.s.rrExtraRound()
	waitLong(t, func() bool { return !heal.roundBusy() }, 10*time.Second)
	if f.probes("b") != 0 || f.probes("c") != 0 || f.probes("d") == 0 {
		t.Fatalf("the search probes candidates only: b %d, c %d, d %d", f.probes("b"), f.probes("c"), f.probes("d"))
	}
}

// W5: with no reason the right starts nothing and waits for one.
func TestRRPause_extraRoundWaitsForAReason(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	heal := &f.s.balancer.heal
	end := time.Now()
	heal.setPauseStep(1)
	heal.setPauseEnd(&end)
	heal.grantExtra(time.Now(), time.Now())
	f.s.rrExtraRound()
	time.Sleep(50 * time.Millisecond)
	if heal.roundBusy() || f.allProbes() != 0 || !heal.extraOpen() {
		t.Fatal("a right with no reason starts no round and stays open")
	}
}

// N3: a device or network wake is an event; the callback goes with the group.
func TestRRHeal_wakeFreezes(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	f.g.ctx = ctx
	manager := service.FromContext[pause.Manager](pause.WithDefaultManager(context.Background()))
	f.g.pause = manager
	f.g.rrWatchWake()
	heal := &f.s.balancer.heal
	for _, wake := range []func(){
		func() { manager.DevicePause(); manager.DeviceWake() },
		func() { manager.NetworkPause(); manager.NetworkWake() },
	} {
		heal.epochEnd.Store(0)
		wake()
		if !heal.frozen() {
			t.Fatal("a wake freezes the self-heal")
		}
	}
	heal.epochEnd.Store(0)
	cancel()
	time.Sleep(20 * time.Millisecond)
	manager.DevicePause()
	manager.DeviceWake()
	if heal.frozen() {
		t.Fatal("a closed group takes no wake")
	}
}

// S3: a suspicion lives 2T from the latest sign; a proof that started at or after
// its sign lifts it, an earlier one does not; E lifts it; so do D1 and the run.
func TestRRTrack_S3(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b"}, []string{"a", "b"}, 0)
	track := f.s.balancer.trackOf("a")
	now := rrClock(time.Now())
	a := now - int64(time.Second)
	track.suspectAt(a, now)
	if !track.suspected(now, math.MinInt64) {
		t.Fatal("no suspicion")
	}
	if track.suspected(now+rrHorizon(), math.MinInt64) || !track.suspected(now+rrHorizon()-1, math.MinInt64) {
		t.Fatal("the suspicion lives 2T")
	}
	if track.suspected(now, a+1) {
		t.Fatal("a sign before E sets no suspicion")
	}
	f.s.rrProof(track, a-1, now)
	if !track.suspected(now, math.MinInt64) {
		t.Fatal("a proof that started before the sign does not lift it")
	}
	f.s.rrProof(track, a, now)
	if track.suspected(now, math.MinInt64) {
		t.Fatal("a proof that started at the sign lifts it")
	}
	track.suspectAt(now, now)
	f.s.balancer.rrScheduledRun()
	if track.suspected(now, math.MinInt64) {
		t.Fatal("the scheduled run lifts the suspicion")
	}
	track.suspectAt(now, now)
	f.s.rrDead(track)
	if track.suspected(now, math.MinInt64) {
		t.Fatal("D1 lifts the suspicion")
	}
}

// lx:end SPEC 138
