package wireguard

import "github.com/sagernet/sing-box/adapter"

var _ adapter.StaleRebindable = (*Endpoint)(nil) // lx: SPEC 041 v2 wake nudge

// RebindStale is the SPEC 041 v2 wake-nudge entry for one endpoint: the
// consumer (LxBox BoxService on USER_PRESENT via libbox
// RebindStaleEndpoints) reported that the device just woke up, so a session
// whose keys are provably dead should heal NOW — rebind the socket and
// re-initiate — instead of waiting ~15-90s for traffic demand to walk the
// retry cycle into the early/give-up triggers.
//
// Sleep-compatibility gates (SPEC 020): resumeMu serialises against the idle
// tick and dial wakes; !started covers idle-asleep, torn-down,
// deliberately-stopped and closed endpoints alike, so a sleeper is never
// woken and never rebound. The stale predicate, the shared trigger debounce
// and the rebind itself (async, in the device's goroutine) live in the
// wireguard-go device — a healthy session is a strict no-op here.
func (w *Endpoint) RebindStale() {
	if w.closing.Load() {
		return
	}
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.closing.Load() || !w.started.Load() {
		return
	}
	w.endpoint.RebindIfSessionStale()
}
