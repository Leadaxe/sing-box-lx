package wireguard

import "testing"

// lx: SPEC 020 — a suspended device is woken by the protocol layer's hook, not
// by the transport on its own; an awake device never reaches the hook.
func TestResume_handsWakeToHook(t *testing.T) {
	endpoint := &Endpoint{}
	var calls int
	endpoint.SetWakeHookLx(func() { calls++ })

	endpoint.resume()
	if calls != 0 {
		t.Fatal("an awake device must not call the wake hook")
	}
	endpoint.suspended.Store(true)
	endpoint.resume()
	if calls != 1 {
		t.Fatalf("a suspended device must hand its wake to the hook once, got %d", calls)
	}
	if !endpoint.suspended.Load() {
		t.Fatal("the transport must not clear the flag itself: the hook's Resume does")
	}
}
