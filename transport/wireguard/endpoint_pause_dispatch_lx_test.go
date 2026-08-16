// lx: SPEC 071 — pause events must never block the pause manager. sing's
// defaultManager runs callbacks while holding its own lock, so a callback
// blocked on device mutexes (field dump: Down() behind a dead-detour bind
// close) froze pause/wake/network delivery for the whole process. These tests
// pin the detached-dispatch mechanics: the callback returns immediately, a
// burst coalesces to its final event, and a shutdown stamp invalidates queued
// applications.
package wireguard

import (
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/service/pause"
)

// With pauseOpAccess held (a stuck device stand-in), onPauseUpdated must
// return immediately — the pause manager's goroutine is never captive.
func TestOnPauseUpdatedNeverBlocks(t *testing.T) {
	e := &Endpoint{}
	e.pauseOpAccess.Lock()
	start := time.Now()
	for i := 0; i < 100; i++ {
		e.onPauseUpdated(pause.EventDevicePaused)
	}
	elapsed := time.Since(start)
	e.pauseOpAccess.Unlock()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("onPauseUpdated blocked for %v with the device stuck — pause manager would freeze", elapsed)
	}
}

// A burst of events queued behind a stuck device applies exactly once, with
// the last event: out-of-order goroutine scheduling must not park the device
// in a stale state.
func TestPauseEventsLatestWins(t *testing.T) {
	e := &Endpoint{}
	var appliedMu sync.Mutex
	var applied []int
	e.pauseApplyHookForTest = func(event int) {
		appliedMu.Lock()
		applied = append(applied, event)
		appliedMu.Unlock()
	}

	e.pauseOpAccess.Lock()
	e.onPauseUpdated(pause.EventDevicePaused)
	e.onPauseUpdated(pause.EventDeviceWake)
	e.onPauseUpdated(pause.EventNetworkPause)
	e.onPauseUpdated(pause.EventNetworkWake) // final state
	e.pauseOpAccess.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for {
		appliedMu.Lock()
		snapshot := append([]int(nil), applied...)
		appliedMu.Unlock()
		if len(snapshot) > 0 {
			// Give stragglers a beat to (incorrectly) apply, then assert.
			time.Sleep(100 * time.Millisecond)
			appliedMu.Lock()
			final := append([]int(nil), applied...)
			appliedMu.Unlock()
			if len(final) != 1 || final[0] != pause.EventNetworkWake {
				t.Fatalf("want exactly the final event applied once, got %v", final)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no event applied at all")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stamp bumped under pauseOpAccess (what Close/Teardown do while shutting
// the device down) invalidates every queued application.
func TestPauseEventInvalidatedByShutdownStamp(t *testing.T) {
	e := &Endpoint{}
	var appliedMu sync.Mutex
	var applied []int
	e.pauseApplyHookForTest = func(event int) {
		appliedMu.Lock()
		applied = append(applied, event)
		appliedMu.Unlock()
	}

	e.pauseOpAccess.Lock()
	e.onPauseUpdated(pause.EventDeviceWake) // queued behind the lock
	e.pauseSeq.Add(1)                       // the shutdown bump
	e.pauseOpAccess.Unlock()

	time.Sleep(300 * time.Millisecond)
	appliedMu.Lock()
	defer appliedMu.Unlock()
	if len(applied) != 0 {
		t.Fatalf("queued event applied after the shutdown stamp: %v", applied)
	}
}

// Close and Teardown must actually bump the stamp (the invalidation the test
// above relies on) — and be safe on a nil-device endpoint.
func TestCloseAndTeardownBumpPauseStamp(t *testing.T) {
	e := &Endpoint{}
	before := e.pauseSeq.Load()
	if err := e.Close(); err != nil {
		t.Fatalf("Close of a nil-device endpoint must not error: %v", err)
	}
	if e.pauseSeq.Load() != before+1 {
		t.Fatal("Close must bump pauseSeq to invalidate queued pause events")
	}
	e2 := &Endpoint{}
	before2 := e2.pauseSeq.Load()
	e2.Teardown()
	if e2.pauseSeq.Load() != before2+1 {
		t.Fatal("Teardown must bump pauseSeq to invalidate queued pause events")
	}
}
