package group

// lx:begin SPEC 019 round_robin: the pool of the group, its runs and its state
//
// The group's runs set the slots of the pool (urltest_balance_lx.go keeps the
// slots and plans their occupancy): the lazy scheduled run (balancePool), the
// rebuild after a run that tested every node (rebuildPool) and the seed before the
// first run (seedPool). Pool, PoolSlot and Mode are the state of the pool for the
// clients (GetPool, Group.mode).

import (
	"cmp"
	"context"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// rrStart wires the round_robin pool of a starting group: a pool change
// invalidates the SPEC 020 reachable cache (ctx carries the invalidator), the pick
// gets its self-heal (SPEC 138) and a wake freezes the self-heal (N3).
func (s *URLTest) rrStart(group *URLTestGroup) {
	if s.balancer == nil {
		return
	}
	ctx := s.ctx
	s.balancer.onChange = func() {
		invalidateReachability(ctx)
	}
	s.rrWire()
	group.rrWatchWake()
}

// PoolSlot is one entry of the round_robin rotation pool. lx: SPEC 019 v2.
type PoolSlot struct {
	Slot  int
	Tag   string
	Delay uint16 // ms; 0 = not measured / dead. A living node is clamped to >= 1 (see Pool).
	// lx: SPEC 138 — the self-heal state of the node (urltest_rr_status_lx.go).
	Quarantined bool
	Conns       uint32 // connections counted since the node took the slot (halved at 256)
	Fails       uint32 // confirmed failures, halved with Conns
}

// Pool returns the current rotation pool (one entry per slot) for round_robin groups. For
// least_test (nil balancer) it returns nil — "this group has no pool". Delay is read from
// history and clamped 0->1 for live nodes so 0 in the output unambiguously means dead/untested.
// lx: SPEC 019 v2 (exposed to clients via the GetPool RPC).
func (s *URLTest) Pool() []PoolSlot {
	if s.balancer == nil || s.group == nil {
		return nil
	}
	tags := s.balancer.poolTags()
	slots := make([]PoolSlot, len(tags))
	for i, tag := range tags {
		var delay uint16
		// History is keyed by RealTag(detour, tcp) (a nested-group member is tested and
		// stored under its live leaf, not the group tag); read it the same way, or
		// the slot's delay is always 0/dead for group members (SPEC 022 #5). Fall
		// back to the raw slot tag if the outbound can't be resolved.
		historyTag := tag
		if node, loaded := s.outbound.Outbound(tag); loaded {
			historyTag = RealTag(node, N.NetworkTCP)
		}
		if history := s.group.history.LoadURLTestHistory(historyTag); history != nil {
			delay = history.Delay
			if delay == 0 {
				delay = 1 // live sub-ms node: never report 0 (0 is reserved for dead/untested)
			}
		}
		slots[i] = PoolSlot{Slot: i, Tag: tag, Delay: delay}
	}
	s.balancer.rrSlotStates(slots) // lx: SPEC 138
	return slots
}

// Mode reports the group's selection mode, derived from the same balancer != nil
// discriminator Now() and Pool() branch on — the validated option string is not kept after
// newBalancer() resolved it (an empty mode: means least_test). Exposed to clients via the
// Group.mode field so they can tell a balanced group from a least_test one without probing
// GetPool, which is gated behind with_lx_command. lx: SPEC 019 v2.
func (s *URLTest) Mode() string {
	if s.balancer != nil {
		return C.URLTestModeRoundRobin
	}
	if s.failover { // lx: SPEC 116
		return C.URLTestModeFailover
	}
	return C.URLTestModeLeastTest
}

// selectBalanced picks an outbound per-connection in round_robin mode from the balancer's
// fixed-size pool. lx: SPEC 019 v2. fallback (Select's outbounds[0]) covers the cold-start
// window before the first health-check fills the pool. Returns nil only when nothing usable.
func (s *URLTest) selectBalanced(ctx context.Context, network string, destination M.Socksaddr) (adapter.Outbound, rrPick) {
	fallback, _ := s.group.Select(network)
	selected, picked := s.balancer.pickKeyed(ctx, network, destination, fallback, func(tag string) adapter.Outbound { // lx: SPEC 138 — the pick hands its key to the dial
		node, _ := s.outbound.Outbound(tag)
		if node != nil && !common.Contains(node.Network(), network) {
			return nil
		}
		return node
	})
	if selected != nil {
		s.group.balancedPicked(ctx, selected) // lx: SPEC 139 — group stage; a probe leaves Now() alone
	}
	return selected, picked
}

// poolSize is the effective pool size for the current node set: min(configured, available).
func (g *URLTestGroup) poolSize() int {
	return min(g.balancer.poolSize, len(g.outbounds))
}

// balancePool is the per-interval lazy health-check for round_robin. It tests no more nodes
// than needed to keep the pool full of live nodes, then applies the new slot occupancy.
// Returns tag->delay for every node it tested live (for the URLTest map / UI).
// A run with no answer changes nothing (urltest_rr_run_lx.go): its first answer lifts
// the marks and the quarantine and restarts strikes(n) (Q4), and only a run with an
// answer lays the pool out.
func (g *URLTestGroup) balancePool(ctx context.Context) map[string]uint16 {
	lifted := g.balancer.rrRunBegin(true, false) // lx: SPEC 138 — the nodes in quarantine: the run probes them
	defer g.balancer.rrRunEnd()
	size := g.poolSize()
	if size == 0 {
		return map[string]uint16{}
	}
	var result map[string]uint16
	if g.balancer.poolTolerance > 0 {
		result = g.balancePoolTolerant(ctx, size)
	} else {
		result = g.balancePoolFirstLive(ctx, size, lifted)
	}
	if g.balancer.rrRunGotAnswer() {
		g.balancer.unverified.clear() // lx: SPEC 019 — the run probed the unverified nodes with its answer as the control
	}
	return result
}

// balancePoolFirstLive (pool_tolerance == 0): re-test the nodes already in the pool, then walk
// the rest in config order (rrWalkPool), and lay the pool out by planFirstLivePool over the
// slots as they stand at the end of the run: a member with history keeps its slot (also one
// that took it during the run), a hole takes the next live node of the walk not struck out
// during the run, and a dead member keeps its slot until a live replacement is found.
func (g *URLTestGroup) balancePoolFirstLive(ctx context.Context, size int, lifted []string) map[string]uint16 {
	current := g.balancer.poolTags()
	inPool := make(map[string]bool, len(current))
	for _, tag := range current {
		if tag != "" {
			inPool[tag] = true
		}
	}
	result := g.testNodes(ctx, g.outboundsByTags(current), true)
	holes := max(size, len(current))
	for _, tag := range current {
		if _, live := result[tag]; tag != "" && live {
			holes--
		}
	}
	walked, fillOrder := g.rrWalkPool(ctx, size, holes, inPool, result)
	g.rrProbeLifted(ctx, lifted, inPool, walked, result) // lx: SPEC 138 §8 item 19 — the run probes the nodes in quarantine
	if !g.balancer.rrRunGotAnswer() {
		return result // lx: SPEC 019 — a run with no answer lays out nothing
	}
	g.rrReprobePool(ctx, result) // lx: SPEC 019 — the pool batch got no answer, a later one did: its control
	// lx: SPEC 138 §5 — the layout goes from the slots at the end of the run: a node that
	// took a slot during it (rrFillOnAlive) keeps that slot, only empty slots and slots
	// without history fill; a node struck out during the run takes none, the hole goes to
	// the next live node of the walk
	current = g.balancer.rrDropStruck(g.balancer.poolTags())
	live := make(map[string]bool, len(result))
	for tag := range result {
		if !g.balancer.rrRunStruck(tag) {
			live[tag] = true
		}
	}
	for _, tag := range current {
		if tag != "" && !live[tag] && g.rrHasHistory(tag) {
			live[tag] = true // I5: its probe of the run failed, a later answer wrote its history
		}
	}
	g.rrSetRunSlots(planFirstLivePool(current, live, fillOrder, size))
	return result
}

// rrReprobePool probes once more the pool members whose probe of the run failed in a
// batch with no answer (their history came back): a later batch of the run answered,
// so this probe has its control — a member that fails it loses its history and its
// slot fills like any dead one (a pool of 1, a pool that died at once). A member that
// answers keeps its slot (an outage at the start of the run). An unverified member
// (the manual test with no answer erased its history) is probed so too.
func (g *URLTestGroup) rrReprobePool(ctx context.Context, result map[string]uint16) {
	var again []adapter.Outbound
	for _, tag := range g.balancer.poolTags() {
		if _, live := result[tag]; tag == "" || live || g.balancer.rrRunStruck(tag) || !g.rrHasHistory(tag) && !g.rrUnverified(tag) {
			continue
		}
		if node, loaded := g.outbound.Outbound(tag); loaded {
			again = append(again, node)
		}
	}
	if len(again) == 0 {
		return
	}
	for tag, delay := range g.testNodes(ctx, again, true) {
		result[tag] = delay
	}
}

// rrWalkPool walks the nodes out of the pool in config order, testing them in batches of
// at most size, until the holes of the pool are filled and rrPoolReserve more live nodes
// answered (R10): the reserve takes no slot, its fresh history puts it first in a
// replacement search (SPEC 138 §5). It adds the live ones to result and returns the nodes
// it tested and the live ones in walk order.
func (g *URLTestGroup) rrWalkPool(ctx context.Context, size, holes int, inPool map[string]bool, result map[string]uint16) (map[string]bool, []string) {
	reserve := rrPoolReserve(size)
	walked := make(map[string]bool, len(g.outbounds))
	var order []string
	candidates := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if !inPool[detour.Tag()] {
			candidates = append(candidates, detour)
		}
	}
	for start := 0; start < len(candidates); {
		need := max(holes-len(order), 0) + max(reserve-max(len(order)-holes, 0), 0)
		if need == 0 {
			break
		}
		end := min(start+min(size, need), len(candidates))
		batch := candidates[start:end]
		start = end
		tested := g.testNodes(ctx, batch, true)
		for _, detour := range batch {
			tag := detour.Tag()
			walked[tag] = true
			if delay, ok := tested[tag]; ok {
				result[tag] = delay
				order = append(order, tag)
			}
		}
	}
	return walked, order
}

// balancePoolTolerant (pool_tolerance > 0): test all nodes, then pick the top-`size` by delay,
// replacing a pool member only when an outside node beats it by more than the tolerance.
func (g *URLTestGroup) balancePoolTolerant(ctx context.Context, size int) map[string]uint16 {
	result := g.testNodes(ctx, g.outbounds, true)
	if !g.balancer.rrRunGotAnswer() {
		return result // lx: SPEC 019 — a run with no answer lays out nothing
	}
	results := make(map[string]candidate, len(g.outbounds))
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		delay, ok := result[tag]
		results[tag] = candidate{tag: tag, delay: delay, alive: ok && !g.balancer.rrRunStruck(tag)} // lx: SPEC 138 §5 — no node struck out during the run
	}
	next := planTolerantPool(g.balancer.rrDropStruck(g.balancer.poolTags()), results, size, g.balancer.poolTolerance)
	g.rrSetRunSlots(next)
	return result
}

// rebuildPool re-derives slot occupancy after a forced full test (manual URLTest) from the
// history now present. It honours pool_tolerance: with tolerance == 0 it keeps living members
// in their slots (first-live discipline — a manual test must not reshuffle a stable pool and
// break sticky bindings); with tolerance > 0 it re-ranks by delay like the steady-state path.
func (g *URLTestGroup) rebuildPool() {
	g.balancer.rrResetMarks() // lx: SPEC 138 — a manual run or the run after a network change lifts the marks and the quarantine
	size := g.poolSize()
	if size == 0 {
		return
	}
	results := make(map[string]candidate, len(g.outbounds))
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		if history := g.history.LoadURLTestHistory(RealTag(detour, N.NetworkTCP)); history != nil {
			results[tag] = candidate{tag: tag, delay: history.Delay, alive: true}
		} else {
			results[tag] = candidate{tag: tag, alive: false}
		}
	}
	current := g.balancer.poolTags()
	if g.balancer.poolTolerance > 0 {
		g.rrSetRunSlots(planTolerantPool(current, results, size, g.balancer.poolTolerance))
		return
	}
	inPool := make(map[string]bool, len(current))
	for _, tag := range current {
		if tag != "" {
			inPool[tag] = true
		}
	}
	live := make(map[string]bool, len(results))
	// Fill holes with live non-pool nodes, fastest first (history is warm after the force test).
	fillCandidates := make([]candidate, 0, len(results))
	for _, detour := range g.outbounds {
		c := results[detour.Tag()]
		if c.alive {
			live[c.tag] = true
			if !inPool[c.tag] {
				fillCandidates = append(fillCandidates, c)
			}
		}
	}
	sortCandidatesByDelay(fillCandidates)
	fillOrder := make([]string, len(fillCandidates))
	for i, c := range fillCandidates {
		fillOrder[i] = c.tag
	}
	g.rrSetRunSlots(planFirstLivePool(current, live, fillOrder, size))
}

// rrSetRunSlots sets the layout of a run and writes a line for every slot it changed
// (SPEC 138 §5: every write of a slot has its line).
func (g *URLTestGroup) rrSetRunSlots(next []string) {
	before := g.balancer.poolTags()
	g.balancer.setSlots(next)
	for i, tag := range next {
		old := ""
		if i < len(before) {
			old = before[i]
		}
		switch {
		case tag == old:
		case tag == "":
			g.logger.Info("round_robin: the run leaves slot ", i, " of ", old, " empty")
		default:
			g.logger.Info("round_robin: the run puts ", tag, " into slot ", i, " of ", cmp.Or(old, "none"))
		}
	}
}

// seedPool fills the pool before the first health-check: prefer nodes with live history (the
// process was not unloaded), else the first `size` nodes in config order. lx: SPEC 019 v2.
func (g *URLTestGroup) seedPool() {
	if g.balancer == nil {
		return
	}
	size := g.poolSize()
	if size == 0 {
		return
	}
	// Nodes with existing history first (top by delay), then config order to fill.
	withHistory := make([]candidate, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if history := g.history.LoadURLTestHistory(RealTag(detour, N.NetworkTCP)); history != nil {
			withHistory = append(withHistory, candidate{tag: detour.Tag(), delay: history.Delay, alive: true})
		}
	}
	sortCandidatesByDelay(withHistory)
	next := make([]string, 0, size)
	seen := make(map[string]bool)
	for _, c := range withHistory {
		if len(next) >= size {
			break
		}
		next = append(next, c.tag)
		seen[c.tag] = true
	}
	for _, detour := range g.outbounds {
		if len(next) >= size {
			break
		}
		if tag := detour.Tag(); !seen[tag] {
			next = append(next, tag)
			seen[tag] = true
		}
	}
	g.balancer.setSlots(next)
}

// outboundsByTags resolves slot tags back to live outbound objects (skipping empties/unknowns).
func (g *URLTestGroup) outboundsByTags(tags []string) []adapter.Outbound {
	out := make([]adapter.Outbound, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if node, ok := g.outbound.Outbound(tag); ok {
			out = append(out, node)
		}
	}
	return out
}

// lx:end SPEC 019
