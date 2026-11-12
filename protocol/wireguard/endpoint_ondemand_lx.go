package wireguard

import "time"

// lx: SPEC 020 on the 1.15 line — one owner for a WG/AWG device's sleep.
//
// Upstream puts an `on_demand` endpoint to sleep when nothing in the routing
// tree references it or the device is paused (ReferenceManager →
// SetKeepIdleConnections), and wakes a suspended device from the transport's
// dial, listen and packet paths. The fork keeps a sleep state on the same
// device: asleep, torn down, never built, switched off by hand. Upstream's
// calls used to act on the device directly, past that state. Both now go
// through it, so the idle tick, the build budget, the manual switch and
// upstream's on_demand see one device with one state.

// setOnDemandKeepLx is the body of SetKeepIdleConnections.
//
// keep == false suspends at once, without the idle window and without the
// live-traffic gates of the idle tick: that is what on_demand means upstream,
// and the reference manager does not ask twice. The state gates stay — a
// listening endpoint has inbound peers nothing would wake it for, a disabled
// one is already down, a building one is in use.
//
// keep == true wakes only what on_demand itself put to sleep. A lazily built or
// idle-suspended endpoint keeps sleeping until traffic reaches it, and a
// disabled one until it is enabled.
func (w *Endpoint) setOnDemandKeepLx(keep bool) {
	if w.building.Load() {
		return
	}
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.closing.Load() {
		return
	}
	if !keep {
		if w.listenMode || w.disabled.Load() || !w.started.Load() {
			return
		}
		w.started.Store(false)
		w.sleepSince.Store(time.Now().UnixNano())
		w.idleAsleep.Store(true)
		w.onDemandAsleep.Store(true)
		w.endpoint.Suspend()
		w.logger.Info("lx idle: suspend ", w.Tag(), " by=on_demand")
		w.budget.Notify()
		return
	}
	if !w.onDemandAsleep.CompareAndSwap(true, false) {
		return
	}
	w.wakeLocked("on_demand")
}

// wakeByTrafficLx is the transport's wake hook: a dial, a listen or a packet
// reached a suspended device. The protocol-level dial path wakes through
// resumeOnDial before it gets here, so in practice this is the packet path —
// routed packets never dial.
func (w *Endpoint) wakeByTrafficLx() {
	if w.disabled.Load() || w.closing.Load() || w.building.Load() {
		return
	}
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.closing.Load() {
		return
	}
	w.stampActivity()
	w.wakeLocked("traffic")
}

// wakeLocked brings an asleep device up and records it. It does nothing for a
// disabled endpoint or one without a device (torn down, never built): those are
// rebuilt by a dial, through the build budget. Caller holds resumeMu.
func (w *Endpoint) wakeLocked(by string) {
	if w.disabled.Load() || !w.idleAsleep.Load() || w.torndown.Load() || w.neverBuilt.Load() {
		return
	}
	if err := w.endpoint.Resume(); err != nil {
		w.logger.Error("lx idle: wake ", w.Tag(), " failed: ", err)
		return
	}
	w.started.Store(true)
	w.idleAsleep.Store(false)
	w.sleepSince.Store(0)
	w.logger.Info("lx idle: wake ", w.Tag(), " by=", by)
}
