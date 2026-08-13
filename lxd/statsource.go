//go:build with_lxd

package lxd

import (
	"time"

	"github.com/sagernet/sing-box/daemon"
)

// statsSource is the slice of the running service GET /admin/stats needs.
// It is separate from `reloader` and optional: the controller type-asserts for
// it, so unit tests keep passing a bare reloader fake and the endpoint simply
// reports "no core" for them.
type statsSource interface {
	// CoreStats returns the live counters, or ok=false when no core is up.
	CoreStats() (uptime time.Duration, uplink int64, downlink int64, connections int, ok bool)
}

// observableService is the real service as the controller sees it: a reloader
// that also answers CoreStats. Embedding rather than wrapping keeps every
// existing method (StartOrReloadService, CloseService) reachable unchanged.
type observableService struct {
	*daemon.StartedService
}

// CoreStats reads the live counters. The traffic manager belongs to the box
// instance, so every field disappears together when the core goes down —
// hence one `ok` for the whole set rather than a per-field optional.
func (s observableService) CoreStats() (time.Duration, int64, int64, int, bool) {
	instance := s.Instance()
	if instance == nil {
		return 0, 0, 0, 0, false
	}
	manager := instance.TrafficManager()
	if manager == nil {
		return 0, 0, 0, 0, false
	}
	// startedAt is zeroed on stop, so a zero value means "not running" even if
	// an Instance pointer lingers.
	startedAt := s.StartedAt()
	if startedAt.IsZero() {
		return 0, 0, 0, 0, false
	}
	uplink, downlink := manager.Total()
	return time.Since(startedAt), uplink, downlink, manager.ConnectionsLen(), true
}
