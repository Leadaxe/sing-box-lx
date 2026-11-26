package group

// lx:begin SPEC 019 round_robin fills the pool while a run probes
//
// A run of the group's health-check (the first one and every full one) used to set
// the pool only at its end. With a dead starting pool (a subscription of many nodes
// with few alive) the group then routed into dead slots for the whole run — minutes
// with batches of 15 s probes — and the self-heal could not help: it waits for the
// health-check flag the run holds. Now a node outside the pool that answers its
// probe takes a slot whose node is dead (no history) or empty at once, through the
// same point write as a self-heal replacement (balancer.replaceSlot): no node in two
// slots, a live slot is never touched, a quarantined node takes none. The run's own
// plan at its end (balancePoolFirstLive, balancePoolTolerant, rebuildPool) goes from
// the slots as they stand then: a node that took a slot here keeps it (SPEC 138 §5,
// I5), only empty and dead slots fill; with pool_tolerance > 0 a live slot changes
// its node only for one faster by more than the tolerance.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// onProbeAliveLx is the hook of a probe that answered (probeAliveLx, urltest_hooks_lx.go): the
// SPEC 054 proof of life, the SPEC 138 proof of the path (rrRunAnswer), and the
// SPEC 019 fill of a dead slot.
func (g *URLTestGroup) onProbeAliveLx(tag string) {
	g.penaltyReset(tag)
	if g.balancer != nil {
		g.balancer.rrRunAnswered() // SPEC 019 — the first answer of the scheduled run lifts, before the fill looks at the quarantine
	}
	if g.balancer != nil && g.balancer.rr != nil {
		g.balancer.rr.rrRunAnswer(tag)
	}
	g.rrFillOnAlive(tag)
}

// rrFillOnAlive puts the group member tag, which has just answered its probe, into
// the first slot whose node is dead or that is empty, when it holds no slot yet. A
// node struck out while the scheduled run runs takes no slot of that run (§5); the
// slot of an unverified node is not given away.
func (g *URLTestGroup) rrFillOnAlive(tag string) {
	b := g.balancer
	if b == nil || g.ctx.Err() != nil || b.isQuarantined(tag) || b.rrStruckInScheduledRun(tag) {
		return
	}
	member := false
	for _, detour := range g.outbounds {
		if detour.Tag() == tag {
			member = true
			break
		}
	}
	if !member {
		return
	}
	slots := b.slotsSnapshot()
	if slotIndex(slots, tag) >= 0 {
		return
	}
	for _, entry := range slots {
		if entry.tag != "" {
			node, loaded := g.outbound.Outbound(entry.tag)
			if loaded && g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) != nil {
				continue // a live slot stays as it is
			}
			if g.rrUnverified(entry.tag) {
				continue // the run probes its node with a control first (rrReprobePool, rebuildPool)
			}
		}
		if b.replaceSlot(entry.tag, tag) {
			g.logger.Debug("round_robin: ", tag, " answered during the run and takes the dead slot of ", entry.tag)
			return
		}
	}
}

// rrPoolReserve is the reserve of the lazy run of a pool of size slots: live nodes
// beyond the pool the run walks to, ceil(size/2) and at least 1.
// They take no slot: their fresh history puts them first in a replacement search
// (SPEC 138 §5); a candidate still takes a slot only by its own probe.
func rrPoolReserve(size int) int {
	return max((size+1)/2, 1)
}

// rrProbeLifted probes the nodes in quarantine at the start of the scheduled run
// (SPEC 138 Q2, Q4) that it has not probed in the pool or in its walk: a quarantined
// node is probed by every run, also with the pool and the reserve full. The first
// answer of the run lifts the quarantine (rrRunAnswered).
func (g *URLTestGroup) rrProbeLifted(ctx context.Context, lifted []string, inPool, walked map[string]bool, result map[string]uint16) {
	var probe []adapter.Outbound
	for _, tag := range lifted {
		if inPool[tag] || walked[tag] {
			continue
		}
		if node, loaded := g.outbound.Outbound(tag); loaded {
			probe = append(probe, node)
		}
	}
	if len(probe) == 0 {
		return
	}
	for tag, delay := range g.testNodes(ctx, probe, true) {
		result[tag] = delay
	}
}

// lx:end SPEC 019
