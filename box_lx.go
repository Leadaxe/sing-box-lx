package box

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxmtu"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

// applyLXOptions resolves the root `lx` block (lx: SPEC 098): the deprecated
// route.lx_idle_* aliases are folded into lx.wg.* with one warning per key,
// the block is validated, and the resolved values are registered in the box
// context for the router, WG endpoints and masque outbounds, all of which are
// created after this call. options is left in canonical form.
//
// It then runs the path-MTU alignment pre-pass (lx: SPEC 120): every node
// above an IP tunnel through `detour` gets its mtu / initial_packet_size
// aligned in the typed option structs, before the constructors read them.
//
// A value is registered even when the block is absent, so a registry shared
// across instances (daemon reloads) never hands a stale block to a new box.
func applyLXOptions(ctx context.Context, options *option.Options, logger log.Logger) (context.Context, error) {
	resolved, warnings, err := option.ResolveLX(options)
	if err != nil {
		return ctx, err
	}
	for _, warning := range warnings {
		logger.Warn(warning)
	}
	if err := alignPathMTU(options, resolved.MTUAlignOrDefault(), logger); err != nil {
		return ctx, err
	}
	ctx = service.ContextWithPtr(ctx, resolved)
	// lx: SPEC 097 — a fresh build-budget slot per box; the WG endpoints fill
	// it on first use (see adapter.LXBuildBudgetSlot).
	return service.ContextWithPtr(ctx, &adapter.LXBuildBudgetSlot{}), nil
}

// alignPathMTU is the `detour` side of SPEC 120 §3: one Info line per
// changed field, one Warn line per warning, nothing at all in mode off. An
// unknown tag in lx.mtu_align.except fails the start.
func alignPathMTU(options *option.Options, align option.LXMTUAlignResolved, logger log.Logger) error {
	policy, err := lxmtu.NewPolicy(align.Mode, align.Except)
	if err != nil {
		return E.Cause(err, "lx.mtu_align")
	}
	if policy.Mode == lxmtu.ModeOff {
		return nil
	}
	decisions, err := lxmtu.NewResolver(options.Outbounds, options.Endpoints, policy).Align()
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		if decision.Changed {
			logger.Info("mtu_align: ", decision.String())
		}
		if decision.Warning != "" {
			logger.Warn("mtu_align: ", decision.Warning)
		}
	}
	return nil
}
