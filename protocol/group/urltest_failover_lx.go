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
//   - a held node that answers but degraded (SPEC 126: slower than
//     failoverTrigger × the fastest other node's last delay, confirmed by an
//     immediate second probe) escalates the tick the same way; the network moves
//     only if the fresh full run found a node failoverMove × faster;
//   - a forced run (manual URLTest, SPEC 054 valve) tests all nodes and
//     re-selects the fastest.
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
	slow := make(tagSet, len(held))
	if len(held) > 0 {
		result = g.testNodes(ctx, held, false)
		if ctx.Err() != nil {
			return result
		}
		for _, node := range held {
			if g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) == nil {
				failed[node.Tag()] = true
			}
		}
		// lx: SPEC 126 — a live held node far slower than the others is confirmed
		// by a second probe; confirmed slow escalates like a failure.
		for _, node := range held {
			if failed[node.Tag()] || !g.failoverDegraded(node) {
				continue
			}
			for tag, delay := range g.probeNow(ctx, node) {
				result[tag] = delay
			}
			if ctx.Err() != nil {
				return result
			}
			if g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) == nil {
				failed[node.Tag()] = true
			} else if g.failoverDegraded(node) {
				slow[node.Tag()] = true
			}
		}
		if len(failed) == 0 && len(slow) == 0 {
			return result
		}
	}
	// Escalation: the failed or slow held node was just probed, skip it in the full run.
	others := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if !failed[detour.Tag()] && !slow[detour.Tag()] {
			others = append(others, detour)
		}
	}
	for tag, delay := range g.testNodes(ctx, others, false) {
		result[tag] = delay
	}
	// reselect=false: the network whose held node still answers keeps it; the
	// failed one (no history any more) moves to the fastest live node; the slow
	// one moves only to a node failoverMove × faster (failoverSelect).
	if len(slow) > 0 {
		g.failoverSlow.Store(&slow)
		defer g.failoverSlow.Store(nil)
	}
	g.performSelectionUpdate(false)
	return result
}

// tagSet is a set of outbound tags.
type tagSet map[string]bool

// SPEC 126 degradation thresholds, as num/den ratios of delays.
const (
	// failoverTrigger: the held node is suspect when slower than 2× the fastest
	// other node's last known delay (possibly stale — only a reason to look).
	failoverTriggerNum, failoverTriggerDen = 2, 1
	// failoverMove: after a confirmed trigger and a fresh full run, the network
	// moves only to a node at least 1.5× faster than the held one.
	failoverMoveNum, failoverMoveDen = 3, 2
)

// failoverDegraded reports whether a live held node is slower than
// failoverTrigger × the fastest other node with history that supports a network
// the node is held for. No such node — false.
func (g *URLTestGroup) failoverDegraded(node adapter.Outbound) bool {
	history := g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP))
	if history == nil {
		return false
	}
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		held := g.selectedOutboundTCP
		if network == N.NetworkUDP {
			held = g.selectedOutboundUDP
		}
		if held != node {
			continue
		}
		var best uint16
		found := false
		for _, detour := range g.outbounds {
			if detour == node || !supportsNetwork(detour, network) {
				continue
			}
			other := g.history.LoadURLTestHistory(RealTag(detour, N.NetworkTCP))
			if other == nil {
				continue
			}
			if !found || other.Delay < best {
				best, found = other.Delay, true
			}
		}
		if found && slowerBy(history.Delay, best, failoverTriggerNum, failoverTriggerDen) {
			return true
		}
	}
	return false
}

// slowerBy reports delay > num/den × than.
func slowerBy(delay, than uint16, num, den int) bool {
	return int(delay)*den > int(than)*num
}

// probeNow probes one node regardless of its history age (confirmation probe).
func (g *URLTestGroup) probeNow(ctx context.Context, node adapter.Outbound) map[string]uint16 {
	return urlTestOutbounds(ctx, g.outbound, g.history, g.logger, []adapter.Outbound{node}, g.link, 0, false, g.penaltyReset)
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
		if current != nil {
			if history := g.history.LoadURLTestHistory(RealTag(current, N.NetworkTCP)); history != nil {
				// lx: SPEC 126 — a confirmed-slow held node yields only to a node
				// failoverMove × faster in the fresh full run.
				if slow := g.failoverSlow.Load(); slow != nil && (*slow)[current.Tag()] {
					if fastest := g.fastestByDelay(network); fastest != nil && fastest != current {
						if best := g.history.LoadURLTestHistory(RealTag(fastest, N.NetworkTCP)); best != nil && slowerBy(history.Delay, best.Delay, failoverMoveNum, failoverMoveDen) {
							return fastest, true
						}
					}
				}
				return current, true
			}
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
