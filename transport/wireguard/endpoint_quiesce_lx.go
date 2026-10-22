package wireguard

// lx: SPEC 030 — the shutdown pre-pass, as an explicit call.
//
// box.Close used to lean on a device-pause broadcast to bring every WireGuard
// device Down before the close pass. Upstream 6364d9a5f ("Fix WireGuard
// endpoint stopping on device sleep") removed the device-pause reaction from
// onPauseUpdated on purpose — a WireGuard endpoint now lives through device
// sleep, and SPEC 020 relies on that — so the broadcast no longer closes
// anything. QuiesceForShutdown is the replacement: the router calls it on each
// WG/AWG endpoint, and it brings the device Down (BindClose releases the UDP or
// detour socket and joins the receive workers) without closing it, leaving the
// teardown to Close.
//
// It applies the way a pause event does (SPEC 071): asynchronously, under
// pauseOpAccess, stamped with pauseSeq. The caller is box.Close walking every
// endpoint, so a Down that blocks behind a dead detour (the SPEC 071 field dump)
// must not serialise the walk; and the stamp means a pause event queued before
// the quiesce never brings the device back Up behind it, while a Close that
// overtakes the quiesce invalidates it the same way it invalidates a pause
// event. Close then runs Down itself, so nothing depends on the quiesce having
// landed first — it only makes the close pass cheaper when it has.
func (e *Endpoint) QuiesceForShutdown() {
	seq := e.pauseSeq.Add(1)
	go func() {
		e.pauseOpAccess.Lock()
		defer e.pauseOpAccess.Unlock()
		if e.pauseSeq.Load() != seq {
			return // Close/Teardown or a newer event already own the device
		}
		if e.device != nil {
			e.device.Down()
		}
	}()
}

// PauseSeqForTest exposes the pause/quiesce stamp so the protocol layer's tests
// can see that a pre-pass reached the transport. Test-only.
func (e *Endpoint) PauseSeqForTest() uint64 {
	return e.pauseSeq.Load()
}
