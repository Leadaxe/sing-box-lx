package box

import (
	"context"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// applyLXOptions resolves the root `lx` block (lx: SPEC 098): the deprecated
// route.lx_idle_* aliases are folded into lx.wg.* with one warning per key,
// the block is validated, and the resolved values are registered in the box
// context for the router, WG endpoints and masque outbounds, all of which are
// created after this call. options is left in canonical form.
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
	return service.ContextWithPtr(ctx, resolved), nil
}
