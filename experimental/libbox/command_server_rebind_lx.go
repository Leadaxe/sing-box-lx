package libbox

import "github.com/sagernet/sing-box/adapter"

// RebindStaleEndpoints is the SPEC 041 v2 wake nudge — the WG/AWG self-heal
// mirror of ResetNetwork. The consumer (LxBox BoxService on USER_PRESENT)
// calls it when the device wakes so every WG/AWG endpoint whose session is
// provably dead rebinds its socket and re-initiates immediately, collapsing
// the post-wake ERR window to one handshake RTT instead of waiting ~15-90s
// for traffic demand to reach the early/give-up triggers. Healthy, sleeping
// (SPEC 020) and stopped endpoints are strict no-ops, so a broadcast storm on
// the native side is harmless (the rebind itself is debounced per device).
// Never blocks the caller: the walk runs in one goroutine per call, so the
// gomobile thread is released immediately.
func (s *CommandServer) RebindStaleEndpoints() {
	instance := s.StartedService.Instance()
	if instance == nil || instance.Box() == nil {
		return
	}
	endpointManager := instance.Box().Endpoint()
	if endpointManager == nil {
		return
	}
	go func() {
		for _, ep := range endpointManager.Endpoints() {
			if rebindable, ok := ep.(adapter.StaleRebindable); ok {
				rebindable.RebindStale()
			}
		}
	}()
}
