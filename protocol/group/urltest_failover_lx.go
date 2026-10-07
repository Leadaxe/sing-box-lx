package group

// lx:begin SPEC 116 failover mode
//
// mode: failover holds the working node until it fails:
//   - the first selection, and every selection after a failure, is the fastest
//     node with history (no tolerance; list order does not matter);
//   - while the held node answers, the periodic tick probes only the held nodes
//     (TCP selection and, if different, UDP selection), so one node wakes per
//     interval instead of N;
//   - a held node that stops answering escalates the tick to a full run over all
//     nodes, and the failed network moves to the fastest live node; the network
//     whose held node still answers keeps it;
//   - a "path dead" dial failure moves the selection through the SPEC 054
//     machinery (penaltyFailoverDial → moveSelection), unchanged;
//   - a forced run (manual URLTest, SPEC 054 valve) tests all nodes and
//     re-selects the fastest — the only way back to the best node while the held
//     one is alive.
//
// DialContext/ListenPacket are unchanged: pickForDial reads the held selection
// from selectedOutbound*, exactly as in least_test.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// failoverHeld returns the held nodes (TCP selection, then UDP selection if it
// differs). Empty before the first selection.
func (g *URLTestGroup) failoverHeld() []adapter.Outbound {
	held := make([]adapter.Outbound, 0, 2)
	if g.selectedOutboundTCP != nil {
		held = append(held, g.selectedOutboundTCP)
	}
	if g.selectedOutboundUDP != nil && g.selectedOutboundUDP != g.selectedOutboundTCP {
		held = append(held, g.selectedOutboundUDP)
	}
	return held
}

// failoverCheck is the periodic (non-forced) health-check of a failover group:
// probe the held nodes only; if none is held yet or any of them failed, run the
// full probe over the remaining nodes and let the failed network move on.
func (g *URLTestGroup) failoverCheck(ctx context.Context) map[string]uint16 {
	held := g.failoverHeld()
	result := make(map[string]uint16)
	failed := make(map[string]bool, len(held))
	if len(held) > 0 {
		result = g.testNodes(ctx, held, false)
		if ctx.Err() != nil {
			return result
		}
		for _, node := range held {
			if g.history.LoadURLTestHistory(RealTag(g.outbound, node)) == nil {
				failed[node.Tag()] = true
			}
		}
		if len(failed) == 0 {
			return result
		}
	}
	// Escalation: the failed held node was just probed, skip it in the full run.
	others := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if !failed[detour.Tag()] {
			others = append(others, detour)
		}
	}
	for tag, delay := range g.testNodes(ctx, others, false) {
		result[tag] = delay
	}
	// reselect=false: the network whose held node still answers keeps it; the
	// failed one (no history any more) moves to the fastest live node.
	g.performSelectionUpdate(false)
	return result
}

// failoverSelect is the failover selection used by performSelectionUpdate.
func (g *URLTestGroup) failoverSelect(network string, reselect bool) (adapter.Outbound, bool) {
	// SPEC 054 emergency mode ranks by penalties first, as in least_test.
	if g.penaltyEmergency(network) {
		if best := g.penaltyBest(network, ""); best != nil {
			return best, true
		}
	}
	if !reselect {
		var current adapter.Outbound
		switch network {
		case N.NetworkTCP:
			current = g.selectedOutboundTCP
		case N.NetworkUDP:
			current = g.selectedOutboundUDP
		}
		if current != nil && g.history.LoadURLTestHistory(RealTag(g.outbound, current)) != nil {
			return current, true
		}
	}
	if fastest := g.fastestByDelay(network); fastest != nil {
		return fastest, true
	}
	// Nothing has history yet: upstream cold start (first node supporting the network).
	return g.Select(network)
}

// selectForUpdate dispatches the selection policy for performSelectionUpdate:
// failover (SPEC 116) or the penalty-aware upstream Select (SPEC 054).
func (g *URLTestGroup) selectForUpdate(network string, reselect bool) (adapter.Outbound, bool) {
	if g.failover {
		return g.failoverSelect(network, reselect)
	}
	return g.selectPenaltyAware(network)
}

// lx:end SPEC 116
