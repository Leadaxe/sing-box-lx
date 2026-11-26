package box

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxmtu"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

// applyLXOptions resolves the root `lx` block (lx: SPEC 098): the block is
// validated and the resolved values are registered in the box context for
// the router, WG endpoints and masque outbounds, all of which are created
// after this call. options is left in canonical form.
//
// Then the pre-pass, in this order (lx: SPEC 165 ALGORITHM §7.1): the chain
// rewrite of `detour` (SPEC 165), the path-MTU alignment of every node above
// an IP tunnel (SPEC 120, lx.detour.mtu_align), the sniff guard built over
// the rewritten detour graph (SPEC 060). The effective config and the
// changes per node are registered for GetEffectiveConfig/GetOutboundConfig.
//
// A value is registered even when the block is absent, so a registry shared
// across instances (daemon reloads) never hands a stale block to a new box.
func applyLXOptions(ctx context.Context, options *option.Options, logger log.Logger) (context.Context, error) {
	var rawSniffGuard *option.LXSniffGuardOptions
	if options.LX != nil {
		rawSniffGuard = options.LX.SniffGuard
	}
	resolved, err := option.ResolveLX(options)
	if err != nil {
		return ctx, err
	}
	changes, err := option.RewriteLXChains(ctx, options)
	if err != nil {
		return ctx, err
	}
	if changes == nil {
		changes = make(map[string][]option.LXConfigChange)
	}
	warnChainGroups(options, logger)
	if err := checkDetourExcept(options, resolved.Detour); err != nil {
		return ctx, err
	}
	// Д1: recorded now, in the order of the changes; taken out of the
	// effective view only, the options go to upstream as they are.
	if err := recordIgnoredUnderDetour(options, changes); err != nil {
		return ctx, err
	}
	if err := alignPathMTU(options, resolved.Detour, changes, logger); err != nil {
		return ctx, err
	}
	resolved.DetourNodes = detourNodes(options, resolved.Detour)
	resolved.DetourRecordFragmentSet = recordFragmentSet(options, resolved.DetourNodes)
	resolved.DetourLogged = new(sync.Map)
	// lx: SPEC 060 — the guard of this box, registered even when off (nil) for
	// the same reason as resolved below.
	guard, err := newSniffGuard(ctx, options, rawSniffGuard, resolved.SniffGuard, logger)
	if err != nil {
		return ctx, err
	}
	expandLXDetour(options, resolved.Detour)
	view, err := withoutIgnoredUnderDetour(*options)
	if err != nil {
		return ctx, err
	}
	service.MustRegister[*option.LXEffectiveConfig](ctx, option.NewLXEffectiveConfig(view, changes))
	ctx = lxsniffguard.ContextWith(ctx, guard)
	ctx = service.ContextWithPtr(ctx, resolved)
	// lx: SPEC 097 — a fresh build-budget slot per box; the WG endpoints fill
	// it on first use (see adapter.LXBuildBudgetSlot).
	return service.ContextWithPtr(ctx, &adapter.LXBuildBudgetSlot{}), nil
}

// checkDetourExcept fails the start on an unknown tag in
// lx.detour.except_outbounds (SPEC 165 Р8), whatever the modes.
func checkDetourExcept(options *option.Options, detour option.LXDetourResolved) error {
	if len(detour.ExceptOutbounds) == 0 {
		return nil
	}
	known := make(map[string]bool, len(options.Outbounds)+len(options.Endpoints))
	for _, outbound := range options.Outbounds {
		known[outbound.Tag] = true
	}
	for _, endpoint := range options.Endpoints {
		known[endpoint.Tag] = true
	}
	for _, tag := range detour.ExceptOutbounds {
		if !known[tag] {
			return E.New("lx.detour.except_outbounds: unknown outbound/endpoint tag: ", tag)
		}
	}
	return nil
}

// detourNodes is the set of nodes the lx.detour.strip filters act on: a node
// with `detour` after the rewrite, not in except_outbounds (SPEC 165 С1). nil
// when every filter is off.
func detourNodes(options *option.Options, detour option.LXDetourResolved) map[string]bool {
	if !detour.Strip.Any() {
		return nil
	}
	nodes := make(map[string]bool)
	add := func(tag string, rawOptions any) {
		wrapper, ok := rawOptions.(option.DialerOptionsWrapper)
		if !ok || wrapper.TakeDialerOptions().Detour == "" || detour.Excepted(tag) {
			return
		}
		nodes[tag] = true
	}
	for _, outbound := range options.Outbounds {
		add(outbound.Tag, outbound.Options)
	}
	for _, endpoint := range options.Endpoints {
		add(endpoint.Tag, endpoint.Options)
	}
	return nodes
}

// recordFragmentSet is the nodes of nodes whose top-level TLS has an explicit
// record_fragment (SPEC 165 С3 with SPEC 156).
func recordFragmentSet(options *option.Options, nodes map[string]bool) map[string]bool {
	if len(nodes) == 0 {
		return nil
	}
	set := make(map[string]bool)
	for _, outbound := range options.Outbounds {
		if !nodes[outbound.Tag] {
			continue
		}
		if wrapper, ok := outbound.Options.(option.OutboundTLSOptionsWrapper); ok {
			if tls := wrapper.TakeOutboundTLSOptions(); tls != nil && tls.RecordFragment != nil {
				set[outbound.Tag] = true
			}
		}
	}
	return set
}

// expandLXDetour writes lx.detour expanded into the effective config (Р9).
// options.LX is replaced, never written through.
func expandLXDetour(options *option.Options, detour option.LXDetourResolved) {
	var lx option.LXOptions
	if options.LX != nil {
		lx = *options.LX
	}
	lx.Detour = option.ExpandedLXDetour(detour)
	options.LX = &lx
}

// alignPathMTU is the `detour` side of SPEC 120 §3: one Info line per
// changed field, one Warn line per warning, nothing at all in mode off. Each
// changed field is also a config change (SPEC 165 М4).
func alignPathMTU(options *option.Options, detour option.LXDetourResolved, changes map[string][]option.LXConfigChange, logger log.Logger) error {
	policy, err := lxmtu.NewPolicy(detour.MTUAlign, detour.ExceptOutbounds)
	if err != nil {
		return E.Cause(err, "lx.detour.mtu_align")
	}
	if policy.Mode == lxmtu.ModeOff {
		return nil
	}
	copyAlignable(options)
	decisions, err := lxmtu.NewResolver(options.Outbounds, options.Endpoints, policy).Align()
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		if decision.Changed {
			logger.Info("mtu_align: ", decision.String())
			changes[decision.Tag] = append(changes[decision.Tag], mtuAlignChanges(decision, policy.Mode.String())...)
		}
		if decision.Warning != "" {
			logger.Warn("mtu_align: ", decision.Warning)
		}
	}
	return nil
}

// mtuAlignChanges turns a changed decision into its config changes: the field
// itself and, for QUIC, disable_path_mtu_discovery turned on.
func mtuAlignChanges(decision lxmtu.Decision, mode string) []option.LXConfigChange {
	by := option.LXConfigChangeBy{
		Type: option.LXChangeByMTUAlign, Mode: mode,
		Below: decision.Below, Overhead: decision.Overhead, Reason: decision.String(),
	}
	change := option.LXConfigChange{Field: decision.Field, To: decision.Effective, By: by}
	if decision.Explicit {
		change.From = decision.Configured
	}
	result := []option.LXConfigChange{change}
	if decision.PMTUDDisabled {
		result = append(result, option.LXConfigChange{Field: "disable_path_mtu_discovery", To: true, By: by})
	}
	return result
}

// copyAlignable gives every node the alignment may write (tunnels and QUIC
// nodes) its own copy of the options struct, so the caller's structs stay
// as written: the running config, a second box on the same Options (SPEC 165
// И4). The fields the alignment writes are plain values of these structs.
func copyAlignable(options *option.Options) {
	outbounds := append([]option.Outbound(nil), options.Outbounds...)
	for i := range outbounds {
		switch typed := outbounds[i].Options.(type) {
		case *option.MASQUEOutboundOptions:
			copied := *typed
			outbounds[i].Options = &copied
		case *option.Hysteria2OutboundOptions:
			copied := *typed
			outbounds[i].Options = &copied
		case *option.TUICOutboundOptions:
			copied := *typed
			outbounds[i].Options = &copied
		case *option.HysteriaOutboundOptions:
			copied := *typed
			outbounds[i].Options = &copied
		}
	}
	endpoints := append([]option.Endpoint(nil), options.Endpoints...)
	for i := range endpoints {
		if typed, isWireGuard := endpoints[i].Options.(*option.WireGuardEndpointOptions); isWireGuard {
			copied := *typed
			endpoints[i].Options = &copied
		}
	}
	options.Outbounds = outbounds
	options.Endpoints = endpoints
}

// warnChainGroups writes one line naming the groups on chain positions >= 1:
// all their traffic goes through the chain, routes straight to them included
// (SPEC 165 Л3, Т5).
func warnChainGroups(options *option.Options, logger log.Logger) {
	members := make(map[string][]string)
	for _, outbound := range options.Outbounds {
		switch group := outbound.Options.(type) {
		case *option.SelectorOutboundOptions:
			members[outbound.Tag] = group.Outbounds
		case *option.URLTestOutboundOptions:
			members[outbound.Tag] = group.Outbounds
		}
	}
	var taken []string
	for _, outbound := range options.Outbounds {
		hop, isHop := outbound.Options.(*option.ChainHopOutboundOptions)
		if !isHop || hop.Position == 0 {
			continue
		}
		// The group on the position and the groups nested in it.
		seen := make(map[string]bool)
		var walk func(tag string)
		walk = func(tag string) {
			groupMembers, isGroup := members[tag]
			if !isGroup || seen[tag] {
				return
			}
			seen[tag] = true
			taken = append(taken, tag+" ("+hop.Chain+" #"+strconv.Itoa(hop.Position)+")")
			for _, member := range groupMembers {
				walk(member)
			}
		}
		walk(hop.Outbound)
	}
	if len(taken) > 0 {
		logger.Warn("chain: groups on positions >= 1 carry all their traffic through the chain, routes straight to them included: ", strings.Join(taken, ", "))
	}
}
