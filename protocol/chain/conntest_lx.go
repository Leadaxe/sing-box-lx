package chain

import (
	"context"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/common/lxtrace"
)

// traceChain writes the `chain` stage of a connection test (SPEC 139 §4): the
// position count and the nodes the positions resolve to now (ChainPath, which
// skips disabled positions). The stage ends at once: the hops are dialed
// through the groups and links below it, and their own stages (group, detour,
// dial) follow as its siblings under the same parent. Without a trace it costs
// one ctx.Value lookup.
func (c *Chain) traceChain(ctx context.Context) {
	if lxtrace.From(ctx) == nil {
		return
	}
	_, stage := lxtrace.Begin(ctx, "chain", c.Tag())
	if stage == nil {
		return
	}
	stage.Attr("positions", strconv.Itoa(len(c.tags)))
	stage.Attr("resolved", strings.Join(c.ChainPath(), ","))
	var disabled []string
	for i := range c.disabled {
		if c.disabled[i].Load() {
			disabled = append(disabled, strconv.Itoa(i))
		}
	}
	if len(disabled) > 0 {
		stage.Attr("disabled", strings.Join(disabled, ","))
	}
	stage.End(nil)
}
