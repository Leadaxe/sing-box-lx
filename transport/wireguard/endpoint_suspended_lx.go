package wireguard

// Suspended reports whether the protocol layer holds the device down (Suspend
// or Teardown without a Resume or Rebuild since). lx: SPEC 106 — lets the
// protocol-side tests see that a toggle reached the device.
func (e *Endpoint) Suspended() bool {
	return e.suspended.Load()
}
