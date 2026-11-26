// lx: Selector.SelectOutbound must switch with one atomic Swap, as upstream
// does, and fire the SPEC 020 reachability invalidation once per real switch.

package group

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
)

type countingInvalidator struct {
	calls atomic.Int32
}

func (c *countingInvalidator) InvalidateReachability() {
	c.calls.Add(1)
}

func newSelectorWithInvalidator(t *testing.T) (*Selector, *countingInvalidator) {
	t.Helper()
	sel, _, _ := newSelectorUnderTest(t, false)
	invalidator := &countingInvalidator{}
	sel.ctx = service.ContextWith[adapter.ReachabilityInvalidator](sel.ctx, invalidator)
	return sel, invalidator
}

func TestLxSelectorInvalidatesOnRealSwitchOnly(t *testing.T) {
	sel, invalidator := newSelectorWithInvalidator(t)

	steps := []struct {
		tag  string
		want int32
	}{
		{"node-b", 1},
		{"node-b", 1}, // same member: no switch, no invalidation
		{"node-a", 2},
		{"node-a", 2},
	}
	for _, step := range steps {
		if !sel.SelectOutbound(step.tag) {
			t.Fatalf("SelectOutbound(%q) returned false", step.tag)
		}
		if got := invalidator.calls.Load(); got != step.want {
			t.Fatalf("after SelectOutbound(%q): %d invalidations, want %d", step.tag, got, step.want)
		}
	}
	if sel.SelectOutbound("missing") {
		t.Fatal("SelectOutbound of an unknown tag returned true")
	}
	if got := invalidator.calls.Load(); got != 2 {
		t.Fatalf("unknown tag invalidated: %d, want 2", got)
	}
}

// Concurrent callers (clash API, daemon, libbox command server) asking for the
// same member must produce exactly one switch: one invalidation, one interrupt.
// With a non-atomic Load+Store several of them see the old member and all
// run the switch side effects.
func TestLxSelectorConcurrentSameTargetSwitchesOnce(t *testing.T) {
	sel, invalidator := newSelectorWithInvalidator(t)

	const (
		rounds  = 200
		callers = 16
	)
	for round := 0; round < rounds; round++ {
		target := "node-b"
		if round%2 == 1 {
			target = "node-a"
		}
		before := invalidator.calls.Load()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sel.SelectOutbound(target)
			}()
		}
		close(start)
		wg.Wait()
		if got := invalidator.calls.Load() - before; got != 1 {
			t.Fatalf("round %d: %d concurrent switches to %q ran the switch %d times, want 1", round, callers, target, got)
		}
		if now := sel.selected.Load().Tag(); now != target {
			t.Fatalf("round %d: selected %q, want %q", round, now, target)
		}
	}
}

// Concurrent switches to different members: a smoke test for -race. The final
// member is one of them, and a changed selection was invalidated at least once.
func TestLxSelectorConcurrentDifferentTargets(t *testing.T) {
	sel, invalidator := newSelectorWithInvalidator(t)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		tag := "node-a"
		if i%2 == 0 {
			tag = "node-b"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sel.SelectOutbound(tag)
		}()
	}
	wg.Wait()
	if now := sel.selected.Load().Tag(); now != "node-a" && now != "node-b" {
		t.Fatalf("selected %q", now)
	}
	if invalidator.calls.Load() == 0 && sel.selected.Load().Tag() != "node-a" {
		t.Fatal("selection changed without an invalidation")
	}
}
