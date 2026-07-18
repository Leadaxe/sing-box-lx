package adapter

import (
	"context"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	N "github.com/sagernet/sing/common/network"
)

// Note: for proxy protocols, outbound creates early connections by default.

type Outbound interface {
	Type() string
	Tag() string
	Network() []string
	Dependencies() []string
	N.Dialer
}

type OutboundWithPreferredRoutes interface {
	Outbound
	PreferredDomain(metadata *InboundContext, domain string) bool
	PreferredAddress(metadata *InboundContext, address netip.Addr) bool
}

type FlowOutbound interface {
	Outbound
	tun.Port
	PreMatchFlow(network string, destination netip.Addr) PreMatchAction
}

type OutboundRegistry interface {
	option.OutboundOptionsRegistry
	CreateOutbound(ctx context.Context, router Router, logger log.ContextLogger, tag string, outboundType string, options any) (Outbound, error)
}

type OutboundManager interface {
	Lifecycle
	Outbounds() []Outbound
	Outbound(tag string) (Outbound, bool)
	Default() Outbound
	Remove(tag string) error
	Create(ctx context.Context, router Router, logger log.ContextLogger, tag string, outboundType string, options any) error
}

// lx:begin idle-suspend
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
}

// lx:end idle-suspend
