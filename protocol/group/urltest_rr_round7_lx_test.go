// lx:begin SPEC 138 / SPEC 019 round_robin: a proxy's failed open shifts no key, a
// failed open makes the slot suspect, the pool fills while a run probes

package group

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	N "github.com/sagernet/sing/common/network"
)

// 1. A failed open through a proxy leaf (EOF, reset: no answer from the proxy about
// the destination) shifts no key; the retry got the first byte and a answers its
// probe — the failure is counted to a. A refusal the proxy reports (a SOCKS
// failure code: the stand's scenario E) still shifts the key.
func TestRRProxyFailure_noShiftButCount(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].siteDown.Store(true) // the node's server drops the open; its probe answers
	key := f.keyOn(t, "site.example", 0)
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("the retry through b answers")
	}
	f.waitCheck(t)
	if shift, _ := f.shift(key, "site.example"); shift != 0 {
		t.Fatalf("a failed open through a proxy shifts no key, got %d", shift)
	}
	if f.fails("a") != 1 {
		t.Fatalf("it is counted to a (the retry got the first byte, a is alive), got %d", f.fails("a"))
	}

	g := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	g.fb["a"].set(&g.fb["a"].openFail, "site.example", true) // the proxy answers: destination refused
	key = g.keyOn(t, "site.example", 0)
	g.dial(t, key, "site.example")
	g.waitCheck(t)
	if shift, _ := g.shift(key, "site.example"); shift != 1 {
		t.Fatalf("a refusal the proxy reports shifts the key, got %d", shift)
	}
}

// 3. The first failed open through a node puts its slot under suspicion at once:
// while the check runs, new connections go round it; the node's answer lifts it.
func TestRRProxyFailure_slotSuspectAtOnce(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].siteDown.Store(true)
	hold := make(chan struct{})
	f.fb["a"].hold = hold // the check's probe of a waits
	key := f.keyOn(t, "site.example", 0)
	f.dial(t, key, "site.example")
	waitUntil(t, func() bool { return f.probes("a") == 1 })
	for i := 0; i < 5; i++ {
		if selected, _ := f.s.selectBalanced(f.keyOn(t, fmt.Sprint("other", i, ".example"), 0), N.NetworkTCP, siteAddr("other.example")); selected.Tag() == "a" {
			t.Fatal("while the check runs, new connections go round a")
		}
	}
	close(hold)
	f.waitCheck(t)
	if f.s.balancer.trackOf("a").suspect(rrClock(time.Now()), f.s.balancer.heal.epoch()) {
		t.Fatal("a answered its probe: the suspicion lifts")
	}
}

// 4. A cold start with a dead starting pool: 60 nodes, only the last three answer.
// The first one to answer takes a dead slot as soon as it answers, while the run
// still probes; the end of the run sets the pool in its order.
func TestRRFill_deadStartingPoolFillsDuringTheRun(t *testing.T) {
	tags := make([]string, 60)
	for i := range tags {
		tags[i] = fmt.Sprintf("n%02d", i)
	}
	f := newHealFixture(t, tags, tags[:3], 0)
	for i, tag := range tags {
		f.g.history.DeleteURLTestHistory(tag)
		if i < 57 {
			f.nodes[tag].dead.Store(true)
		}
	}
	f.nodes["n57"].slow = 50 * time.Millisecond
	f.nodes["n58"].slow = 400 * time.Millisecond
	f.nodes["n59"].slow = 800 * time.Millisecond
	// Picks go on all the time, in parallel (meaningful under -race).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f.s.selectBalanced(context.Background(), N.NetworkTCP, siteAddr("site.example"))
		}
	}()
	done := make(chan struct{})
	go func() {
		f.g.CheckOutbounds(f.g.ctx, false)
		close(done)
	}()
	waitUntil(t, func() bool { return slotIndex(f.g.balancer.slotsSnapshot(), "n57") >= 0 })
	select {
	case <-done:
		t.Fatal("n57 must take a slot while the run still probes, not at its end")
	default:
	}
	<-done
	close(stop)
	wg.Wait()
	pool := f.pool()
	for _, tag := range []string{"n57", "n58", "n59"} {
		if slotIndex(f.g.balancer.slotsSnapshot(), tag) < 0 {
			t.Fatalf("the end of the run: %s in the pool, got %v", tag, pool)
		}
	}
}

// 4. A healthy starting pool: the run changes nothing.
func TestRRFill_healthyPoolUnchanged(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"}, 0)
	f.g.CheckOutbounds(f.g.ctx, false)
	if !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a healthy pool stays as it is, got %v", f.pool())
	}
}

// lx:end SPEC 138 / SPEC 019
