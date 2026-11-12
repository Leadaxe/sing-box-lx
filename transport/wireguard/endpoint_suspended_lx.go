package wireguard

// Suspended reports whether the protocol layer holds the device down (Suspend
// or Teardown without a Resume or Rebuild since). lx: SPEC 106 — lets the
// protocol-side tests see that a toggle reached the device.
func (e *Endpoint) Suspended() bool {
	return e.suspended.Load()
}

// SetWakeHookLx hands a suspended device's wake to the protocol layer. Upstream
// brings a suspended device up from its own dial, listen and packet paths; the
// protocol layer keeps the sleep state (asleep, torn down, never built, switched
// off by hand — SPEC 020, 097, 106) and must be the one to decide and to record
// it, or a packet would wake an endpoint the state still calls asleep, or one a
// client switched off. Set once, before Start.
func (e *Endpoint) SetWakeHookLx(hook func()) {
	e.wakeHookLx = hook
}

// wakeLx runs the hook in place of upstream's own wake. False when no hook is
// set: upstream's behaviour stands.
func (e *Endpoint) wakeLx() bool {
	if e.wakeHookLx == nil {
		return false
	}
	e.wakeHookLx()
	return true
}
