// lx: SPEC 071 — pause events must never block the pause manager. sing's
// defaultManager runs callbacks while holding its own lock, so a callback
// blocked on device mutexes (field dump: Down() behind a dead-detour bind
// close) froze pause/wake/network delivery for the whole process. Upstream 1.15
// applies the event on its own goroutine under stateAccess; these tests pin
// that the callback returns at once and that a shut-down endpoint ignores a
// late event.
package wireguard

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing/service/pause"
)

// With stateAccess held (a stuck device stand-in), onPauseUpdated must return
// immediately — the pause manager's goroutine is never captive.
func TestOnPauseUpdatedNeverBlocks(t *testing.T) {
	e := &Endpoint{pause: pause.ManagerFromContext(pause.WithDefaultManager(context.Background()))}
	e.stateAccess.Lock()
	start := time.Now()
	for i := 0; i < 100; i++ {
		e.onPauseUpdated(pause.EventNetworkPause)
	}
	elapsed := time.Since(start)
	e.stateAccess.Unlock()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("onPauseUpdated blocked for %v with the device stuck — pause manager would freeze", elapsed)
	}
}

// Close and Teardown release the device under stateAccess, so a pause event
// that lands after them finds no device and is a no-op — and both are safe on
// a nil-device endpoint.
func TestPauseEventAfterShutdownIsNoop(t *testing.T) {
	e := &Endpoint{pause: pause.ManagerFromContext(pause.WithDefaultManager(context.Background()))}
	if err := e.Close(); err != nil {
		t.Fatalf("Close of a nil-device endpoint must not error: %v", err)
	}
	e.updateNetworkPause()
	e2 := &Endpoint{pause: pause.ManagerFromContext(pause.WithDefaultManager(context.Background()))}
	e2.Teardown()
	e2.updateNetworkPause()
	if e2.device.Load() != nil {
		t.Fatal("Teardown must release the device")
	}
}
