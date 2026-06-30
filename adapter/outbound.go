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
	PreferredDomain(domain string) bool
	PreferredAddress(address netip.Addr) bool
}

type DirectRouteOutbound interface {
	Outbound
	NewDirectRouteConnection(metadata InboundContext, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error)
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
	// lx:begin awg
	// ConsumersOf returns the tags of outbounds that depend on (detour through)
	// the given tag — the reverse of Dependencies(). Used by the selector guard to
	// walk up to AmneziaWG consumers when a group switches to a WireGuard member.
	ConsumersOf(tag string) []string
	// lx:end awg
}

// lx:begin awg
// AmneziaWGSuspendable is implemented by an AmneziaWG endpoint so the selector
// guard can suspend it (bring its device down) when a group it detours through
// switches to a WireGuard member — AmneziaWG inside a WireGuard tunnel hangs the
// kernel on Android. The marker lives in adapter so protocol/group can act on it
// without importing protocol/wireguard.
type AmneziaWGSuspendable interface {
	// IsAmneziaWG reports whether this endpoint runs AmneziaWG (has AWG params).
	IsAmneziaWG() bool
	// SuspendAmneziaWG brings the device down so no junk handshake is sent. It is
	// idempotent and safe to call on a not-yet-started or already-suspended endpoint.
	SuspendAmneziaWG()
}

// lx:end awg

// lx:begin idle-suspend
// IdleSuspendable is implemented by a WG/AWG endpoint so the router's idle tick
// (SPEC 020) can suspend it when it is idle and unreachable, without importing
// protocol/wireguard. SuspendIfIdle brings the device Down (freeing its
// recv-worker bufsArrs — the dominant GC-scan holder) only on the live→asleep
// transition; the next dial through the endpoint wakes it lazily.
type IdleSuspendable interface {
	Tag() string
	SuspendIfIdle(reachable bool, threshold time.Duration)
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

// lx:end idle-suspend
