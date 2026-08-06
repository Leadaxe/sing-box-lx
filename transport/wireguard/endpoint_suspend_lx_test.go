// lx:begin idle-suspend

package wireguard

import (
	"testing"

	"github.com/sagernet/sing/service/pause"
)

// TestOnPauseUpdated_wakeSkipsSuspendedDevice pins the SPEC 020/007 pause fix:
// a device the protocol layer holds down (idle-suspend or AWG guard) must NOT be
// brought Up by a device/network wake event. The endpoint here has a nil device,
// so reaching e.device.Up() would nil-panic — the test passing at all proves the
// suspended gate short-circuits first.
func TestOnPauseUpdated_wakeSkipsSuspendedDevice(t *testing.T) {
	e := &Endpoint{}
	e.Suspend() // protocol layer put the device down (nil device → no-op Down)
	e.onPauseUpdated(pause.EventDeviceWake)
	e.onPauseUpdated(pause.EventNetworkWake)
	if !e.suspended.Load() {
		t.Fatal("wake events must not clear the suspended state")
	}
	// Resume (the dial-wake path) clears the flag again.
	if err := e.Resume(); err != nil {
		t.Fatalf("Resume on a nil device must be a no-op, got %v", err)
	}
	if e.suspended.Load() {
		t.Fatal("Resume must clear the suspended state")
	}
}

// TestTransferTotalsNilDevice: no device → 0, never panics.
func TestTransferTotalsNilDevice(t *testing.T) {
	e := &Endpoint{}
	if got := e.TransferTotals(); got != 0 {
		t.Fatalf("nil device must report 0 transfer, got %d", got)
	}
}

// lx:end idle-suspend
