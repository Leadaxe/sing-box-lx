package group

// lx:begin SPEC 125 tick freshness
//
// A non-forced run skips a node whose history is younger than the freshness
// window. Upstream uses interval as the window, but history is stamped when the
// probe ends while the ticker fires exactly every interval, so at the next tick
// a node probed by the previous tick is interval minus the probe time old and is
// skipped: every node was probed once per 2×interval (least_test and failover).
//
// The window is the time since the previous tick run ended, capped at interval:
// history written by the previous tick run is never fresh, history written after
// it (a manual URLTest, a nested group's own ticker) still is. Forced runs keep
// upstream semantics and do not move the mark.

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// testNodesTick is testNodes for a non-forced (ticker) run.
func (g *URLTestGroup) testNodesTick(ctx context.Context, outbounds []adapter.Outbound) map[string]uint16 {
	var lastEnd time.Time
	if nanos := g.lastTickRunEnd.Load(); nanos != 0 {
		lastEnd = time.Unix(0, nanos)
	}
	window := tickFreshWindow(g.interval, lastEnd, time.Now())
	result := urlTestOutbounds(ctx, g.outbound, g.history, g.logger, outbounds, g.link, window, false, g.penaltyReset)
	g.lastTickRunEnd.Store(time.Now().UnixNano())
	return result
}

// tickFreshWindow returns the freshness window for a tick run: the time since
// the previous tick run ended, at most interval; interval before the first run.
func tickFreshWindow(interval time.Duration, lastEnd, now time.Time) time.Duration {
	if lastEnd.IsZero() {
		return interval
	}
	since := now.Sub(lastEnd)
	if since < 0 {
		return 0
	}
	if since > interval {
		return interval
	}
	return since
}

// lx:end SPEC 125
