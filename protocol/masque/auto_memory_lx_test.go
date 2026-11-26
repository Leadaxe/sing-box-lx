package masque

// lx: SPECS/TASKS/121 — what `auto` remembers after h2 wins, and for how long.
// The window climbs a ladder: 1 s for the first h2 win (in practice "retry h3
// on the next bring-up"), the next rung on every further h2 win, 10 min at the
// top, back to the first rung after an h3 win.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A fast remote refusal of h3 must not park the node on h2: the first h2 win
// is remembered only for the initial window, so the next tunnel tries h3 again.
func TestAutoFirstH2WinIsRememberedBriefly(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = time.Second
	o.autoH2Ladder = []time.Duration{30 * time.Millisecond, time.Second}
	o.legsForTest = scripted(&calls, errors.New("http3: parsing frame failed: PROTOCOL_VIOLATION (remote)"), false, nil)

	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h2" {
		t.Fatalf("h2 must carry this tunnel: network=%q err=%v", network, err)
	}
	if strings.Join(calls, ",") != "h3,h2" {
		t.Fatalf("expected h3 then h2: %v", calls)
	}
	memory := o.autoNetwork.Load()
	if memory == nil || memory.network != "h2" || memory.window != 30*time.Millisecond {
		t.Fatalf("first h2 win must be remembered for the initial window, got %+v", memory)
	}
	time.Sleep(50 * time.Millisecond)
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("after the initial window the next bring-up must start with h3, got %q", got)
	}
}

// Every further h2 win takes the next rung, the last rung repeats; reusing a
// remembered h2 does not push the window; an h3 win drops back to the first rung.
func TestAutoH2WindowClimbsLadderAndResets(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 10 * time.Millisecond
	o.autoH2Ladder = []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 80 * time.Millisecond}
	o.legsForTest = scripted(&calls, nil, true, nil) // h3 silent

	windows := []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond}
	for i, want := range windows {
		calls = nil
		if _, _, network, err := o.connect(context.Background(), o.effectiveNetwork()); err != nil || network != "h2" {
			t.Fatalf("round %d: silent h3 must fall back to h2: network=%q err=%v", i, network, err)
		}
		if strings.Join(calls, ",") != "h3,h2" {
			t.Fatalf("round %d: after expiry h3 must be probed first: %v", i, calls)
		}
		memory := o.autoNetwork.Load()
		if memory == nil || memory.window != want {
			t.Fatalf("round %d: window = %v, want %v", i, memory.window, want)
		}
		if i == 0 {
			// Reusing the remembered h2 keeps the same memory and window.
			calls = nil
			if _, _, network, err := o.connect(context.Background(), o.effectiveNetwork()); err != nil || network != "h2" {
				t.Fatalf("remembered h2 must be reused: network=%q err=%v", network, err)
			}
			if strings.Join(calls, ",") != "h2" {
				t.Fatalf("remembered h2 must go straight to h2: %v", calls)
			}
			if o.autoNetwork.Load() != memory {
				t.Fatal("reusing h2 must not refresh the memory window")
			}
		}
		time.Sleep(want + 15*time.Millisecond)
		if got := o.effectiveNetwork(); got != "h3" {
			t.Fatalf("round %d: expired h2 memory must yield h3, got %q", i, got)
		}
	}

	// h3 comes back: remembered without a window, backoff reset.
	calls = nil
	o.legsForTest = scripted(&calls, nil, false, errors.New("h2 must not be dialled"))
	if _, _, network, err := o.connect(context.Background(), o.effectiveNetwork()); err != nil || network != "h3" {
		t.Fatalf("h3 success must win: network=%q err=%v", network, err)
	}
	if memory := o.autoNetwork.Load(); memory == nil || memory.network != "h3" || !memory.expires.IsZero() {
		t.Fatalf("h3 must be remembered without expiry, got %+v", memory)
	}
	if o.autoH2Rung.Load() != 0 {
		t.Fatal("an h3 win must drop the ladder back to the first rung")
	}
	o.legsForTest = scripted(&calls, nil, true, nil)
	if _, _, _, err := o.connect(context.Background(), "h3"); err != nil {
		t.Fatal(err)
	}
	if memory := o.autoNetwork.Load(); memory == nil || memory.window != 20*time.Millisecond {
		t.Fatalf("after a reset the next h2 win must start from the first rung, got %+v", memory)
	}
}

// Without the wiring (no ladder) h2 is remembered without a window, as before
// SPEC 121 — the decision tests built that way keep their meaning.
func TestAutoH2WindowOffWhenUnwired(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.rememberNetwork("h2")
	if memory := o.autoNetwork.Load(); memory == nil || !memory.expires.IsZero() {
		t.Fatalf("unwired outbound must remember h2 without expiry, got %+v", memory)
	}
}

// The production ladder: 1 s → 10 s → 30 s → 5 min → 10 min. The first rung is
// "retry h3 on the next bring-up"; few rungs, because with a silent h3 each rung
// below the tunnel's rebuild period costs one 3 s probe.
func TestAutoH2LadderIsSane(t *testing.T) {
	if len(autoH2MemoryLadder) < 3 || len(autoH2MemoryLadder) > 8 {
		t.Fatalf("%d rungs; expected a short ladder", len(autoH2MemoryLadder))
	}
	if autoH2MemoryLadder[0] > 5*time.Second {
		t.Fatalf("first rung %v: expected a few seconds at most", autoH2MemoryLadder[0])
	}
	last := autoH2MemoryLadder[len(autoH2MemoryLadder)-1]
	if last < 5*time.Minute || last > time.Hour {
		t.Fatalf("top rung %v: expected minutes", last)
	}
	for i := 1; i < len(autoH2MemoryLadder); i++ {
		if autoH2MemoryLadder[i] <= autoH2MemoryLadder[i-1] {
			t.Fatalf("ladder must climb: %v", autoH2MemoryLadder)
		}
	}
}
