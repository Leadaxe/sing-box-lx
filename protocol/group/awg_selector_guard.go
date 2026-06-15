// lx:begin awg

package group

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// suspendAmneziaWGConsumersOnWireGuardSwitch is called from Selector.SelectOutbound
// BEFORE the switch is committed. If the member about to be selected is — or chains
// down via detour to — a WireGuard-based endpoint (type "wireguard", covering plain
// WG and AmneziaWG), it walks UP from this group to every AmneziaWG endpoint that
// detours through it and suspends each one (brings its device down). Rationale:
// AmneziaWG traffic encapsulated inside a WireGuard tunnel hangs the kernel on
// Android; the static Start-guard cannot cover this because a selector's chosen
// member is only known at runtime.
//
// Called before s.selected is updated, so the race is closed: once the group
// points at the WireGuard member, the AmneziaWG consumers are already suspended
// (started=false) and a concurrent reconnect fails with "not ready" instead of
// sending a junk handshake into WireGuard.
func suspendAmneziaWGConsumersOnWireGuardSwitch(outboundManager adapter.OutboundManager, groupTag string, selected adapter.Outbound) {
	if outboundManager == nil || groupTag == "" {
		return
	}
	if !chainReachesWireGuard(outboundManager, selected, make(map[string]bool)) {
		return
	}
	suspendAmneziaWGConsumers(outboundManager, groupTag, make(map[string]bool))
}

// chainReachesWireGuard reports whether outbound is — or transitively detours
// down to, or (being a group) contains a member that is — a WireGuard-based
// endpoint. visited guards against cycles.
func chainReachesWireGuard(outboundManager adapter.OutboundManager, outbound adapter.Outbound, visited map[string]bool) bool {
	if outbound == nil {
		return false
	}
	tag := outbound.Tag()
	if tag != "" {
		if visited[tag] {
			return false
		}
		visited[tag] = true
	}
	if outbound.Type() == C.TypeWireGuard {
		return true
	}
	// Down the detour chain (vless -> ... -> wireguard).
	for _, dependency := range outbound.Dependencies() {
		if member, loaded := outboundManager.Outbound(dependency); loaded {
			if chainReachesWireGuard(outboundManager, member, visited) {
				return true
			}
		}
	}
	// A nested group: any member reaching WireGuard counts.
	if group, isGroup := outbound.(adapter.OutboundGroup); isGroup {
		for _, memberTag := range group.All() {
			if member, loaded := outboundManager.Outbound(memberTag); loaded {
				if chainReachesWireGuard(outboundManager, member, visited) {
					return true
				}
			}
		}
	}
	return false
}

// suspendAmneziaWGConsumers walks UP from tag via the reverse-dependency ledger
// (ConsumersOf) and suspends every AmneziaWG endpoint that detours through it,
// directly or transitively (e.g. AWG -> vless -> group). visited guards cycles.
func suspendAmneziaWGConsumers(outboundManager adapter.OutboundManager, tag string, visited map[string]bool) {
	for _, consumerTag := range outboundManager.ConsumersOf(tag) {
		if visited[consumerTag] {
			continue
		}
		visited[consumerTag] = true
		consumer, loaded := outboundManager.Outbound(consumerTag)
		if !loaded {
			continue
		}
		if awg, isAWG := consumer.(adapter.AmneziaWGSuspendable); isAWG && awg.IsAmneziaWG() {
			awg.SuspendAmneziaWG()
		}
		// Keep walking up: a non-AWG hop (vless) or a parent group may itself have
		// an AmneziaWG consumer above it.
		suspendAmneziaWGConsumers(outboundManager, consumerTag, visited)
	}
}

// lx:end awg
