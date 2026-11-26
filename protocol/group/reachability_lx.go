// lx:begin idle-suspend
package group

import (
	"context"

	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing/service"
)

// invalidateReachability tells the reference manager (SPEC 020) that the active
// routing tree changed: a pool rebuild of round_robin re-shapes what the group
// references. Since 1.15 upstream's reference manager recomputes on a URL test
// history update, so the lx event points notify the shared history storage. A
// context without one (tests) is a no-op.
func invalidateReachability(ctx context.Context) {
	if history := service.PtrFromContext[urltest.HistoryStorage](ctx); history != nil {
		history.NotifyUpdated()
	}
}

// lx:end idle-suspend
