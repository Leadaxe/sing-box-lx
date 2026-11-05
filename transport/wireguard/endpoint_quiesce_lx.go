package wireguard

// lx: SPEC 030 — the shutdown pre-pass, as an explicit call.
//
// box.Close used to lean on a device-pause broadcast to bring every WireGuard
// device Down before the close pass. Upstream 6364d9a5f ("Fix WireGuard
// endpoint stopping on device sleep") removed the device-pause reaction from
// the pause handler on purpose — a WireGuard endpoint now lives through device
// sleep, and SPEC 020 relies on that — so the broadcast no longer closes
// anything. QuiesceForShutdown is the replacement: the router calls it on each
// WG/AWG endpoint, and it brings the device Down (BindClose releases the UDP or
// detour socket and joins the receive workers) without closing it, leaving the
// teardown to Close.
//
// It applies the way upstream's pause handler does: asynchronously, under
// stateAccess. The caller is box.Close walking every endpoint, so a Down that
// blocks behind a dead detour (the SPEC 071 field dump) must not serialise the
// walk; Close swaps the device out under the same mutex, so a quiesce that
// arrives late finds nothing to bring down. Close then runs Down itself, so
// nothing depends on the quiesce having landed first — it only makes the close
// pass cheaper when it has.
func (e *Endpoint) QuiesceForShutdown() {
	e.quiesceCount.Add(1)
	go func() {
		e.stateAccess.Lock()
		defer e.stateAccess.Unlock()
		if wgDevice := e.device.Load(); wgDevice != nil {
			wgDevice.Down()
		}
	}()
}

// QuiesceCountForTest exposes how many pre-passes were requested so the
// protocol layer's tests can see that one reached the transport. Test-only.
func (e *Endpoint) QuiesceCountForTest() uint32 {
	return e.quiesceCount.Load()
}
