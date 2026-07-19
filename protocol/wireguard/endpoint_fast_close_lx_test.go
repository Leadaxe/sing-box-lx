//go:build with_lx_idle_suspend

// lx: SPEC 030 — unit coverage for the fast-shutdown `closing` gate. When a
// close is pending, resumeOnDial must refuse to resurrect the endpoint (return
// not-dialable) instead of starting a fresh device rebuild + handshake that
// Close would then block on behind resumeMu. These tests exercise the gate
// directly on the nil-device harness, without a real WireGuard device.
package wireguard

import (
	"testing"
	"time"
)

// A close is pending → resumeOnDial returns false even for an idle-asleep
// endpoint it would otherwise wake. This is the load-bearing behaviour: without
// it, an in-flight ping-wake starts a rebuild under resumeMu and Close waits it
// out.
func TestResumeOnDial_abortsWhenClosing(t *testing.T) {
	w := newIdleTestEndpoint()
	w.idleAsleep.Store(true)
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())

	// Sanity: without the closing flag, an idle-asleep endpoint wakes.
	if !w.resumeOnDial() {
		t.Fatal("precondition: idle-asleep endpoint must wake when not closing")
	}

	// Now mark closing and re-suspend; resumeOnDial must refuse.
	w.idleAsleep.Store(true)
	w.closing.Store(true)
	if w.resumeOnDial() {
		t.Fatal("resumeOnDial must return false (not dialable) once closing is set")
	}
}

// The fast-path (not idle-asleep) also honours closing: a closing endpoint is
// never dialable, so a forwarded packet cannot resurrect it mid-close.
func TestResumeOnDial_closingFastPath(t *testing.T) {
	w := newIdleTestEndpoint()
	w.closing.Store(true)
	// idleAsleep is false → fast path; closing must still force false.
	if w.resumeOnDial() {
		t.Fatal("resumeOnDial fast path must return false when closing")
	}
}

// Close sets the closing flag (so a concurrent resumeOnDial aborts). The
// nil-device endpoint's Close is a safe no-op teardown; we assert the flag is
// set as a side effect.
func TestClose_setsClosingFlag(t *testing.T) {
	w := newIdleTestEndpoint()
	if w.closing.Load() {
		t.Fatal("fresh endpoint must not be marked closing")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close of a nil-device endpoint must not error: %v", err)
	}
	if !w.closing.Load() {
		t.Fatal("Close must set the closing flag")
	}
}
