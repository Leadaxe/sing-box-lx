// lx: SPEC 041 v2 — unit tests for the wake-nudge sleep gates on the protocol
// endpoint, on the SPEC 020 idle harness (endpoint_idle_lx_test.go). The
// device-level mechanics (stale predicate, debounce, immediate initiation)
// are covered in the wireguard-go submodule tests.
package wireguard

import (
	"testing"
	"time"
)

// An idle-asleep endpoint must stay asleep: the nudge is a no-op and must not
// wake it (waking would defeat SPEC 020 — the sleeper's session is stale by
// definition after a long sleep).
func TestRebindStale_asleepNotWoken(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("harness: endpoint did not fall asleep")
	}

	w.RebindStale()

	if !w.idleAsleep.Load() || w.started.Load() {
		t.Fatalf("nudge woke a sleeper: idleAsleep=%v started=%v",
			w.idleAsleep.Load(), w.started.Load())
	}
}

// A deliberately-stopped endpoint (started=false, not idle-asleep) is a no-op.
func TestRebindStale_stoppedNoop(t *testing.T) {
	w := newIdleTestEndpoint()
	w.started.Store(false)

	w.RebindStale() // must not panic, must not touch flags

	if w.started.Load() || w.idleAsleep.Load() {
		t.Fatal("nudge changed a stopped endpoint's state")
	}
}

// A closing endpoint aborts before taking any lock or touching the device.
func TestRebindStale_closingNoop(t *testing.T) {
	w := newIdleTestEndpoint()
	w.closing.Store(true)

	w.RebindStale() // must not panic

	if !w.started.Load() {
		t.Fatal("nudge changed a closing endpoint's state")
	}
}

// A started endpoint whose transport device is nil (mid-rebuild shape) must
// not panic: the transport passthrough is nil-safe.
func TestRebindStale_nilDeviceSafe(t *testing.T) {
	w := newIdleTestEndpoint() // harness endpoint has a nil wireguard device
	w.RebindStale()            // must not panic
}
