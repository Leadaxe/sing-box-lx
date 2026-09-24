package adapter

// lx: SPEC 020 idle-suspend interfaces.
//
// These live in their own file on purpose. They were previously inlined in
// outbound.go, where the only thing pulling "time" into that file's import
// block was SuspendIfIdle/TeardownIfSlept below. Upstream's outbound.go has no
// such user, so every large upstream merge auto-resolved the import block to
// their version while keeping our body, dropping "time" and breaking the build
// (it happened in both the 235-commit and the 217-commit merges). Keeping the
// interfaces — and the import they need — in an lx-owned file that upstream
// never touches removes that failure mode entirely.

import (
	"sync"
	"time"
)

// IdleSuspendable is implemented by a WG/AWG endpoint so the router's idle tick
// (SPEC 020) can suspend it when it is idle and unreachable, without importing
// protocol/wireguard. SuspendIfIdle brings the device Down (freeing its
// recv-worker bufsArrs — the dominant GC-scan holder) only on the live→asleep
// transition; the next dial through the endpoint wakes it lazily.
type IdleSuspendable interface {
	Tag() string
	// SuspendIfIdle applies one idle-tick decision. threshold is the idle window
	// for UNREACHABLE endpoints; reachableThreshold (0 = disabled) is the longer
	// window after which even a reachable endpoint suspends (lx_idle_suspend_reachable).
	SuspendIfIdle(reachable bool, threshold time.Duration, reachableThreshold time.Duration)
	// TeardownIfSlept applies the level-3 decision: an endpoint asleep longer than
	// threshold is released completely (device closed, netstack freed) and rebuilt
	// on the next dial. 0 = disabled. lx_idle_teardown (SPEC 020).
	TeardownIfSlept(threshold time.Duration)
}

// ReachabilityInvalidator is implemented by the Router. SPEC 020 reachability is
// recomputed only on events that change the active routing tree — a selector
// switch, a urltest auto-switch / pool rebuild, or a config reload — not on every
// idle tick. Those event points pull this out of the context
// (service.FromContext[adapter.ReachabilityInvalidator]) and mark the cache
// dirty; the next idle tick recomputes lazily. Kept as its own narrow interface
// so protocol/group calls it without importing route and without widening the
// large adapter.Router interface.
type ReachabilityInvalidator interface {
	InvalidateReachability()
}

// ReachabilityReporter is implemented by the Router. A urltest group consults it
// (service.FromContext) before a scheduled health-check: while the group itself
// is unreachable from the active routing tree, probing its members only wakes
// idle-suspended endpoints for nothing (the 30-minute probe tail after a
// selector switches away). Returns true when idle-suspend is off or the build
// lacks the tag — probing is then never gated.
type ReachabilityReporter interface {
	OutboundReachable(tag string) bool
	// SelectionRefs counts the edges of the active routing tree that lead
	// straight into tag (SPEC 097): manual ones come from a selector's current
	// choice, the final outbound and rule targets; auto ones from a urltest
	// pool, a detour, a chain position and a DNS-server detour. No weight is
	// inherited along the tree. The build budget ranks eviction victims by it.
	// (0, 0) without the idle-suspend tag.
	SelectionRefs(tag string) (manual int, auto int)
}

// Endpoint states reported by IdleStateReporter (SPEC 097). "not built" is a
// state, not an error: the device is built by the first dial.
const (
	EndpointStateNeverBuilt = "never_built"
	EndpointStateBuilding   = "building"
	EndpointStateUp         = "up"
	EndpointStateAsleep     = "asleep"
	EndpointStateTornDown   = "torn_down"
	EndpointStateDown       = "down"
)

// IdleState is a snapshot of a WG/AWG endpoint's SPEC 020/097 state machine.
// IdleSince is the time since the last dial through the endpoint (0 if never
// stamped), SleepSince the time it has been asleep (0 when awake).
type IdleState struct {
	State      string
	IdleSince  time.Duration
	SleepSince time.Duration
}

// IdleStateReporter is implemented by WG/AWG endpoints for observability
// (GetOutbounds). The snapshot is read without locks and may lag one
// transition behind.
type IdleStateReporter interface {
	IdleState() IdleState
}

// LXBuildBudgetSlot holds the per-box WG build budget (SPEC 097). box.New
// registers a fresh slot in the context for every box, so a service registry
// shared across daemon reloads never hands one box's budget to the next; the
// WG package fills it on first use, which keeps the box package free of a
// wireguard import. Nil-safe: a nil slot yields nil.
type LXBuildBudgetSlot struct {
	once  sync.Once
	value any
}

// Load returns the slot's value, creating it with create on first use.
func (s *LXBuildBudgetSlot) Load(create func() any) any {
	if s == nil {
		return nil
	}
	s.once.Do(func() { s.value = create() })
	return s.value
}
