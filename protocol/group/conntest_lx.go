package group

// lx: SPEC 139 — connection test hooks of the urltest-based groups (least_test,
// round_robin, failover):
//   - the `group` stage records the per-connection choice: group tag, mode, the
//     selected node and, for round_robin, the slot and the stickiness key;
//   - a probe connection (lxtrace.IsProbe) reads the group state but never
//     writes it (SPEC 139 §5.7): no penalties, no history deletion, no moved
//     selection, no forced retest, no round_robin rotation step and no Now()
//     update. The probe still dials the node the group picked.
// Without a trace in the context every hook costs one ctx.Value lookup
// (SPEC 139 §5.12).

import (
	"context"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
)

// mode reports the selection mode, as URLTest.Mode does.
func (g *URLTestGroup) mode() string {
	if g.balancer != nil {
		return C.URLTestModeRoundRobin
	}
	if g.failover {
		return C.URLTestModeFailover
	}
	return C.URLTestModeLeastTest
}

// traceGroup writes the `group` stage of one pick and ends it at once: the
// dial of the selected node is its sibling, not its child. fallbackFor names
// the node whose failed dial led to this pick (SPEC 054), empty otherwise.
func (g *URLTestGroup) traceGroup(ctx context.Context, selected adapter.Outbound, fallbackFor string) {
	if selected == nil || lxtrace.From(ctx) == nil {
		return
	}
	_, stage := lxtrace.Begin(ctx, "group", g.groupTag)
	if stage == nil {
		return
	}
	stage.Attr("type", C.TypeURLTest)
	stage.Attr("mode", g.mode())
	realTag := RealTag(selected, N.NetworkTCP)
	stage.Attr("selected", realTag)
	if selected.Tag() != realTag {
		// a nested group: selected is its live leaf, member the group itself
		stage.Attr("member", selected.Tag())
	}
	if fallbackFor != "" {
		stage.Attr("fallback_for", fallbackFor)
	}
	if b := g.balancer; b != nil {
		stage.Attr("per_connection", "true")
		slot := -1
		for i, tag := range b.poolTags() {
			if tag == selected.Tag() {
				slot = i
				break
			}
		}
		if slot >= 0 {
			stage.Attr("slot", strconv.Itoa(slot))
		} else {
			// the pool is empty (cold start) or holds no usable node: Select's fallback
			stage.Attr("slot", "fallback")
		}
		if len(b.stickyHash) > 0 {
			stage.Attr("sticky", strings.Join(b.stickyHash, ","))
		}
	}
	if lxtrace.IsProbe(ctx) {
		stage.Attr("probe", "true")
	}
	stage.End(nil)
}

// balancedPicked is the round_robin tail of selectBalanced: the pick goes into
// the connection's SPEC 138 cell, where the connection list reads it; then the
// `group` stage, then the Now() update, which a probe skips.
func (g *URLTestGroup) balancedPicked(ctx context.Context, selected adapter.Outbound) {
	adapter.RecordPickLx(ctx, g.groupTag, selected)
	if lxtrace.From(ctx) != nil {
		g.traceGroup(ctx, selected, "")
		if lxtrace.IsProbe(ctx) {
			return
		}
	}
	g.lastSelected.Store(selected.Tag())
}

// balancedRetrying writes the `group` stage of the SPEC 138 retry through
// another slot before its dial: it names the node whose dial failed in
// fallback_for, as the SPEC 054 fallback does.
func (g *URLTestGroup) balancedRetrying(ctx context.Context, selected adapter.Outbound, failed string) {
	g.traceGroup(ctx, selected, failed)
}

// balancedRetried records an opened SPEC 138 retry: its node replaces the first
// one in the connection's cell and in Now() (a probe leaves Now() alone). A retry
// that did not open is not recorded: the first node stays.
func (g *URLTestGroup) balancedRetried(ctx context.Context, selected adapter.Outbound) {
	adapter.RecordPickLx(ctx, g.groupTag, selected)
	if lxtrace.From(ctx) != nil && lxtrace.IsProbe(ctx) {
		return
	}
	g.lastSelected.Store(selected.Tag())
}

// penaltyResetOnDial is the SPEC 054 "proof of life" of a successful dial; a
// probe leaves the penalties as they are.
func (g *URLTestGroup) penaltyResetOnDial(ctx context.Context, tag string) {
	if lxtrace.IsProbe(ctx) {
		return
	}
	g.penaltyReset(tag)
}

// deleteHistoryOnDialError is the upstream reaction of least_test to a failed
// dial: drop the URL test history of the node's leaf (RealTag, upstream
// 37fffa325), after which the caller re-selects. A probe keeps the history, and
// so does round_robin: a dial error alone proves nothing (SPEC 019 v2, SPEC 138).
// It reports whether the history went.
func (g *URLTestGroup) deleteHistoryOnDialError(ctx context.Context, leaf string) bool {
	if g.balancer != nil || lxtrace.IsProbe(ctx) {
		return false
	}
	g.history.DeleteURLTestHistory(leaf)
	return true
}

// rotate advances the round_robin counter (sticky off) and returns the step to
// use. A probe reads the current step without advancing it, so the next
// production connection gets the node it would have got without the test.
func (b *balancer) rotate(ctx context.Context) uint64 {
	if lxtrace.IsProbe(ctx) {
		return b.counter.Load()
	}
	return b.counter.Add(1) - 1
}
