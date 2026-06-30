// lx:begin idle-suspend
package group

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
)

// invalidateReachability tells the router (SPEC 020) that the active routing tree
// changed, so its cached reachable set must be recomputed. Called from the group
// event points — a selector switch, a urltest auto-switch, a pool rebuild. Pulls
// the invalidator out of the context the same way groups pull other services, so
// protocol/group never imports route. A nil invalidator (feature/route absent) is
// a no-op.
func invalidateReachability(ctx context.Context) {
	if invalidator := service.FromContext[adapter.ReachabilityInvalidator](ctx); invalidator != nil {
		invalidator.InvalidateReachability()
	}
}

// lx:end idle-suspend
