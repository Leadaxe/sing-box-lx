package group

// lx:begin SPEC 138 round_robin self-heal after a failed dial
//
// Stage 1 of SPEC 138 §3: a TCP connection of a round_robin group that does not
// open (any error but a client cancel) is retried once through the next live
// slot of the pool whose node is not a nested urltest (that one retries on its
// own), and the group checks the failed node A and its neighbour B — the next
// live slot, nested or not — in the background:
//   - the probes do not write: a direct urltest.URLTest of the leaf (RealTag)
//     with the group link and C.TCPTimeout; history changes only by verdict, so
//     a blink of the local network does not strike every node at once;
//   - A with history younger than rrHealFresh counts as alive unprobed: the
//     question "is A alive" is answered. A control — B, the third node, the
//     candidates of a replacement search that serves as the control — is always
//     probed live: history says nothing about whether the network answers now;
//   - A silent, B answered: A is dead — its history is dropped (pick skips the
//     slot at once) and the slot gets a replacement from outside the pool; a
//     candidate of that replacement may count by fresh history, A is already
//     proved dead by a live answer;
//   - A answered: A is alive, the destination is not reachable through it; its
//     fresh answer is written. The key shift of §3.5 hooks in here (stage 2);
//   - both silent: a third node answers → both dead; it is silent too → the
//     local network is down, nothing changes and the group freezes;
//   - no neighbour: the replacement search is the control;
//   - the history of a dead node is dropped only if it is not newer than the
//     start of its probe: a scheduled check that found it alive meanwhile wins,
//     and the verdict about that node is dropped.
//
// The replacement runs under URLTestGroup.checking, the flag of the scheduled
// health-check: there is one writer of the slots at a time, and a busy flag
// means a run is already doing that work. It changes only the dead node's slot.
//
// Limits (§3.7): one check in flight per group (its own flag, not checking);
// no new probes for rrHealGap after the previous check ended (retries go on);
// nothing at all — no retries, no probes — for rrHealFreeze after the "own
// network" verdict, a device/network wake or a network change signal
// (URLTest.InterfaceUpdated, whether its full run starts or not), and on top of
// that while the full run it started is in flight. A freeze or the group's
// Close aborts the check in flight at once: its probes run on a context the
// freeze cancels, so it lets URLTestGroup.checking go, and the full run after
// a network change waits for it (bounded) instead of finding the flag busy.
// The marks of the gap and the freeze keep the monotonic clock reading.
//
// The replacement search with pool_tolerance 0 probes the candidates outside the
// pool in parallel batches of the pool size, like balancePoolFirstLive, and takes
// the first one in config order that answered.
//
// A connection test (SPEC 139, lxtrace.IsProbe) gets the retry, with both
// picks in the trace (the retry stage carries fallback_for), and starts no check.

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service/pause"
)

const (
	// rrHealFresh — history younger than this counts as a fresh answer: the node is
	// not probed again.
	rrHealFresh = 30 * time.Second
	// rrHealGap — no new probes this long after the previous check ended.
	rrHealGap = 15 * time.Second
	// rrHealFreeze — no retries and no probes this long after the "own network"
	// verdict or a wake signal.
	rrHealFreeze = 15 * time.Second
	// rrHealYieldWait bounds how long the full run after a network change waits for
	// the aborted self-heal check to end.
	rrHealYieldWait = C.TCPTimeout
)

// rrHeal is the self-heal state of one round_robin group; it lives in the balancer.
// The time marks are time.Now() values: they keep the monotonic clock reading, so
// a wall clock step does not stretch the gap or the freeze.
type rrHeal struct {
	running      atomic.Bool                        // a check is in flight
	lastCheckEnd atomic.Pointer[time.Time]          // end of the previous check
	frozenUntil  atomic.Pointer[time.Time]          // end of the freeze
	networkRuns  atomic.Int32                       // full runs after a network change in flight
	abort        atomic.Pointer[context.CancelFunc] // cancels the check in flight
}

// beginCheck claims the single check slot of the group, unless the previous check
// ended less than rrHealGap ago.
func (h *rrHeal) beginCheck() bool {
	if end := h.lastCheckEnd.Load(); end != nil && time.Since(*end) < rrHealGap {
		return false
	}
	return h.running.CompareAndSwap(false, true)
}

func (h *rrHeal) endCheck() {
	now := time.Now()
	h.lastCheckEnd.Store(&now)
	h.running.Store(false)
}

// freeze freezes the self-heal for rrHealFreeze and aborts the check in flight.
func (h *rrHeal) freeze() {
	until := time.Now().Add(rrHealFreeze)
	h.frozenUntil.Store(&until)
	h.abortCheck()
}

func (h *rrHeal) abortCheck() {
	if cancel := h.abort.Load(); cancel != nil {
		(*cancel)()
	}
}

// frozen reports whether the self-heal is frozen: by the "own network" verdict, a
// wake or a network change signal, or because the full run after a network change
// is in flight.
func (h *rrHeal) frozen() bool {
	if h.networkRuns.Load() > 0 {
		return true
	}
	until := h.frozenUntil.Load()
	return until != nil && time.Now().Before(*until)
}

// aborted reports whether the check running on ctx has to stop: the group closed
// or the self-heal froze since it started.
func (h *rrHeal) aborted(ctx context.Context) bool {
	return ctx.Err() != nil || h.frozen()
}

// yield waits, at most rrHealYieldWait, for the check in flight to end. The
// network change that calls it has frozen the self-heal already, which aborts the
// check and keeps a new one from starting, so the wait is short.
func (h *rrHeal) yield(ctx context.Context) {
	deadline := time.Now().Add(rrHealYieldWait)
	for h.running.Load() && ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// rrNetworkChanged freezes the self-heal for rrHealFreeze from a network change
// signal (InterfaceUpdated). It is called before any early return there: the full
// run may not start (device or network paused, the health-check busy), the network
// is in flux all the same. A no-op outside round_robin.
func (s *URLTest) rrNetworkChanged() {
	if s.balancer != nil {
		s.balancer.heal.freeze()
	}
}

// rrNetworkRun marks the full run after a network change (InterfaceUpdated) for the
// time it runs, so the freeze lasts past rrHealFreeze while the run does, and waits
// (bounded) for the self-heal check in flight to end: it may hold
// URLTestGroup.checking, and the full run would then find the flag busy and skip.
// The returned function ends the mark. A no-op outside round_robin.
func (s *URLTest) rrNetworkRun() func() {
	if s.balancer == nil {
		return func() {}
	}
	heal := &s.balancer.heal
	heal.networkRuns.Add(1)
	heal.abortCheck()
	heal.yield(s.group.ctx)
	return func() {
		heal.networkRuns.Add(-1)
	}
}

// rrWatchWake freezes the self-heal on a device or network wake: the network is not
// up yet, and a failure says nothing about the nodes. The callback is dropped when
// the group closes.
func (g *URLTestGroup) rrWatchWake() {
	if g.balancer == nil || g.pause == nil {
		return
	}
	heal := &g.balancer.heal
	manager := g.pause
	element := manager.RegisterCallback(func(event int) {
		switch event {
		case pause.EventDeviceWake, pause.EventNetworkWake:
			heal.freeze()
		}
	})
	context.AfterFunc(g.ctx, func() {
		manager.UnregisterCallback(element)
	})
}

// rrHealDial handles a failed dial of a round_robin group: it starts the background
// check and returns the connection of the retry through the next live slot.
// ok is false when there is no retry or it failed too: the caller returns dialErr.
func (s *URLTest) rrHealDial(ctx context.Context, network string, destination M.Socksaddr, failed adapter.Outbound, dialErr error) (net.Conn, bool) {
	if N.NetworkName(network) != N.NetworkTCP || errors.Is(dialErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return nil, false
	}
	if s.balancer.heal.frozen() {
		return nil, false
	}
	slots := s.balancer.slotsSnapshot()
	failedSlot := -1
	for i, entry := range slots {
		if entry.tag == failed.Tag() {
			failedSlot = i
			break
		}
	}
	// failed may be the chain hop of the node; the check needs the pool member.
	member, loaded := s.outbound.Outbound(failed.Tag())
	if failedSlot < 0 || !loaded {
		return nil, false // a fallback pick outside the pool (cold start)
	}
	usable := func(tag string) bool {
		return s.slotAlive(tag) && s.slotSupports(tag, network)
	}
	// The control of the check is the next live slot as is: the probe goes to its
	// leaf, a nested urltest included.
	var neighbour adapter.Outbound
	if i, found := s.balancer.nextLiveSlot(slots, failedSlot, usable); found {
		neighbour, _ = s.outbound.Outbound(slots[i].tag)
	}
	if !lxtrace.IsProbe(ctx) && s.balancer.heal.beginCheck() {
		go s.rrHealCheck(member, neighbour)
	}
	// A nested urltest retries on its own (SPEC 054 or this one), so it is neither
	// retried nor taken for the retry: two attempts per call at most. A context
	// that is already done has no time for a second attempt.
	if rrNestedURLTest(member) || ctx.Err() != nil {
		return nil, false
	}
	var retry adapter.Outbound
	if i, found := s.balancer.nextLiveSlot(slots, failedSlot, func(tag string) bool {
		if !usable(tag) {
			return false
		}
		node, loaded := s.outbound.Outbound(tag)
		return loaded && !rrNestedURLTest(node)
	}); found {
		retry, _ = s.outbound.Outbound(slots[i].tag)
	}
	if retry == nil {
		return nil, false
	}
	s.group.balancedRetrying(ctx, retry, failed.Tag())
	dialer, err := adapter.ResolveChainLeaf(ctx, retry)
	if err != nil {
		return nil, false
	}
	conn, err := dialer.DialContext(ctx, network, destination)
	if err != nil {
		s.logger.ErrorContext(ctx, "round_robin retry via ", retry.Tag(), ": ", err)
		return nil, false
	}
	// Only an opened retry moves the connection's node and Now(): a failed one
	// leaves the first node on record.
	s.group.balancedRetried(ctx, retry)
	s.logger.DebugContext(ctx, "round_robin: ", failed.Tag(), " failed, retried via ", retry.Tag())
	return conn, true
}

// slotSupports reports whether a slot's node takes connections of network.
func (s *URLTest) slotSupports(tag string, network string) bool {
	node, loaded := s.outbound.Outbound(tag)
	return loaded && common.Contains(node.Network(), network)
}

// rrNestedURLTest reports whether node, unwrapped through selectors, is a urltest
// group of any mode: it has its own retry, the outer group does not add one.
func rrNestedURLTest(node adapter.Outbound) bool {
	for {
		switch nested := node.(type) {
		case *URLTest:
			return true
		case *Selector:
			node = nested.Selected(N.NetworkTCP)
			if node == nil {
				return false
			}
		default:
			return false
		}
	}
}

// rrProbe is the outcome of one check probe.
type rrProbe struct {
	member   string // the pool member (or candidate) tag
	leaf     string // the tag its history lives under (RealTag)
	answered bool
	fresh    bool // counted alive by fresh history, not probed
	delay    uint16
	err      error
	started  time.Time // when the probe started; a newer history wins over its verdict
}

// rrProbeNode probes the live leaf of member without writing history. With
// byHistory a leaf with history younger than rrHealFresh is not probed and counts
// as an answer; that is only for the question "is this node alive" (the failed
// node A, a candidate of a replacement once A is proved dead). A control is
// probed with byHistory false: it has to show that the network answers now.
func (g *URLTestGroup) rrProbeNode(ctx context.Context, member adapter.Outbound, byHistory bool) rrProbe {
	result := rrProbe{member: member.Tag(), leaf: RealTag(member, N.NetworkTCP), started: time.Now()}
	if result.leaf == "" {
		result.err = E.New("no selected node")
		return result
	}
	if byHistory {
		if history := g.freshHistory(result.leaf); history != nil {
			result.answered, result.fresh, result.delay = true, true, history.Delay
			return result
		}
	}
	leaf, loaded := g.outbound.Outbound(result.leaf)
	if !loaded {
		result.err = E.New("outbound not found: ", result.leaf)
		return result
	}
	testCtx, cancel := context.WithTimeout(ctx, C.TCPTimeout)
	defer cancel()
	done := make(chan urlTestResult, 1)
	go func() {
		delay, err := urltest.URLTest(testCtx, g.link, leaf)
		done <- urlTestResult{delay, err}
	}()
	select {
	case outcome := <-done:
		result.delay, result.err = outcome.delay, outcome.err
	case <-testCtx.Done():
		result.err = testCtx.Err()
	}
	result.answered = result.err == nil
	return result
}

// freshHistory returns the history of leaf if it is younger than rrHealFresh.
func (g *URLTestGroup) freshHistory(leaf string) *adapter.URLTestHistory {
	if history := g.history.LoadURLTestHistory(leaf); history != nil && time.Since(history.Time) < rrHealFresh {
		return history
	}
	return nil
}

// rrFresh reports whether the live leaf of member has fresh history: it counts as
// alive without a probe.
func (g *URLTestGroup) rrFresh(member adapter.Outbound) bool {
	leaf := RealTag(member, N.NetworkTCP)
	return leaf != "" && g.freshHistory(leaf) != nil
}

// rrStore writes the answer of a probe to history; a fresh or silent one is left as is.
func (g *URLTestGroup) rrStore(probe rrProbe) {
	if probe.answered && !probe.fresh {
		g.history.StoreURLTestHistory(probe.leaf, &adapter.URLTestHistory{Time: time.Now(), Delay: probe.delay})
	}
}

// rrHealCheck probes the failed node A and its neighbour B and acts on the verdict
// (SPEC 138 §3.3). It owns the check slot taken by beginCheck. It runs on a
// context that the group's Close and a freeze cancel, and stops at once then.
func (s *URLTest) rrHealCheck(member adapter.Outbound, neighbour adapter.Outbound) {
	g := s.group
	heal := &s.balancer.heal
	defer heal.endCheck()
	ctx, cancel := context.WithCancel(g.ctx)
	heal.abort.Store(&cancel)
	defer func() {
		heal.abort.Store(nil)
		cancel()
	}()
	if heal.aborted(ctx) {
		return
	}
	// A with fresh history is alive: the destination is the problem, no control
	// is needed. The key shift of SPEC 138 §3.5 (stage 2) starts here too.
	if g.rrFresh(member) {
		return
	}
	var a, b rrProbe
	if neighbour != nil {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			b = g.rrProbeNode(ctx, neighbour, false)
		}()
		a = g.rrProbeNode(ctx, member, false)
		wg.Wait()
	} else {
		a = g.rrProbeNode(ctx, member, false)
	}
	// A run cancelled by Close, or a freeze that came in meanwhile (a wake, a network
	// change): the probes say nothing about the nodes.
	if heal.aborted(ctx) || a.leaf == "" {
		return
	}
	if a.answered {
		g.rrStore(a)
		if neighbour != nil {
			g.rrStore(b)
		}
		// A is alive: the destination is not reachable through it. The key shift of
		// SPEC 138 §3.5 (stage 2) starts here.
		return
	}
	if neighbour == nil {
		s.rrReplaceAsControl(ctx, a)
		return
	}
	if b.answered {
		g.rrStore(b)
		s.rrStrike(ctx, []rrProbe{a}, b)
		return
	}
	if third := s.rrThird(member.Tag(), neighbour.Tag()); third != nil {
		c := g.rrProbeNode(ctx, third, false)
		if heal.aborted(ctx) {
			return
		}
		if c.answered {
			g.rrStore(c)
			s.rrStrike(ctx, []rrProbe{a, b}, c)
			return
		}
	}
	s.rrOwnNetwork(a, b)
}

// rrThird returns the third node of a check where A and B are both silent: the next
// live slot after B, else the first node outside the pool (config order).
func (s *URLTest) rrThird(failed, neighbour string) adapter.Outbound {
	slots := s.balancer.slotsSnapshot()
	from := -1
	for i, entry := range slots {
		if entry.tag == neighbour {
			from = i
			break
		}
	}
	if from >= 0 {
		if i, found := s.balancer.nextLiveSlot(slots, from, func(tag string) bool {
			return tag != failed && s.slotAlive(tag) && s.slotSupports(tag, N.NetworkTCP)
		}); found {
			if node, loaded := s.outbound.Outbound(slots[i].tag); loaded {
				return node
			}
		}
	}
	candidates := s.group.rrCandidates(false)
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

// rrOwnNetwork is the verdict "nobody answered": the local network is down; nothing
// changes and the self-heal freezes.
func (s *URLTest) rrOwnNetwork(probes ...rrProbe) {
	s.balancer.heal.freeze()
	s.logger.Debug("round_robin: no node answered the check (", rrProbeSummary(probes...), "), local network assumed down; self-heal frozen for ", rrHealFreeze)
}

// rrDropHistory drops the history of a node found dead, unless it is newer than
// the start of the probe that found it so: a scheduled check answered for the node
// meanwhile, and the verdict about it is dropped. It reports whether the verdict
// stands.
func (s *URLTest) rrDropHistory(dead rrProbe) bool {
	if s.group.history.DeleteURLTestHistoryNotAfterLx(dead.leaf, dead.started) {
		return true
	}
	s.logger.Debug("round_robin: ", dead.member, " answered a scheduled check during the self-heal probe; dead verdict dropped")
	return false
}

// rrTakeChecking claims URLTestGroup.checking for a slot change of the self-heal;
// it fails when the flag is busy (a run does the same work) or the check has been
// aborted, in which case the flag is not held.
func (s *URLTest) rrTakeChecking(ctx context.Context) bool {
	if !s.group.checking.CompareAndSwap(false, true) {
		return false
	}
	if s.balancer.heal.aborted(ctx) {
		s.group.checking.Store(false)
		return false
	}
	return true
}

// rrStrike is the verdict "dead": the history of every dead node is dropped (pick
// skips its slot from now on) and its slot gets a replacement.
func (s *URLTest) rrStrike(ctx context.Context, dead []rrProbe, control rrProbe) {
	struck := make([]rrProbe, 0, len(dead))
	for _, probe := range dead {
		if !s.rrDropHistory(probe) {
			continue
		}
		struck = append(struck, probe)
		s.logger.Info("round_robin: ", probe.member, " struck out as dead: ", rrProbeSummary(probe), "; control ", rrProbeSummary(control))
	}
	if len(struck) == 0 || !s.rrTakeChecking(ctx) {
		return // nothing to replace, or the scheduled health-check is running and does the same work
	}
	defer s.group.checking.Store(false)
	for _, probe := range struck {
		if s.balancer.heal.aborted(ctx) {
			return
		}
		s.rrReplace(ctx, probe, false)
	}
}

// rrReplaceAsControl is the check with no neighbour in the pool: the replacement
// search is the control. A candidate that answers proves A dead and takes its slot;
// nobody answering is the local network.
func (s *URLTest) rrReplaceAsControl(ctx context.Context, a rrProbe) {
	if !s.rrTakeChecking(ctx) {
		return
	}
	defer s.group.checking.Store(false)
	candidate, probed, found := s.rrReplace(ctx, a, true)
	if s.balancer.heal.aborted(ctx) {
		return
	}
	if found {
		s.logger.Info("round_robin: ", a.member, " struck out as dead: ", rrProbeSummary(a), "; control ", rrProbeSummary(candidate))
		return
	}
	if candidate.answered {
		return // the verdict was dropped: A answered a scheduled check meanwhile
	}
	if probed > 0 {
		s.rrOwnNetwork(a)
	}
}

// rrReplace puts a node from outside the pool into the slot of dead (SPEC 138 §3.4).
// It runs under URLTestGroup.checking. pool_tolerance 0 probes the candidates in
// config order, in parallel batches of the pool size (as balancePoolFirstLive does),
// and takes the first one in config order that answered in the first batch with an
// answer; a candidate with fresh history counts as an answer unprobed: dead is
// already proved dead by a live control. pool_tolerance > 0 takes the fastest one
// by history without probes and probes only when none has history. As a control
// (asControl) it always probes live: history of any age is no answer from the
// network now; a candidate that answered proves dead dead, and its history is
// dropped then (rrDropHistory: unless newer than its probe). It returns the answer
// of the replacement, how many candidates it probed and whether a replacement took
// the slot; an answer with found false means the verdict was dropped.
func (s *URLTest) rrReplace(ctx context.Context, dead rrProbe, asControl bool) (rrProbe, int, bool) {
	g := s.group
	if !asControl && g.history.LoadURLTestHistory(dead.leaf) != nil {
		return rrProbe{}, 0, false // probed alive again since the verdict: keep it
	}
	tolerant := g.balancer.poolTolerance > 0 && !asControl
	candidates := g.rrCandidates(tolerant)
	if len(candidates) == 0 {
		return rrProbe{}, 0, false
	}
	var (
		answer rrProbe
		chosen adapter.Outbound
		probed int
	)
	if tolerant {
		// The candidates with history come first, fastest first.
		leaf := RealTag(candidates[0], N.NetworkTCP)
		if history := g.history.LoadURLTestHistory(leaf); history != nil {
			answer = rrProbe{member: candidates[0].Tag(), leaf: leaf, answered: true, fresh: true, delay: history.Delay}
			chosen = candidates[0]
		}
	}
	if chosen == nil {
		answer, chosen, probed = g.rrProbeFirstAnswer(ctx, candidates, g.poolSize(), !asControl, &s.balancer.heal)
		if s.balancer.heal.aborted(ctx) || chosen == nil {
			return rrProbe{}, probed, false
		}
	}
	if asControl && !s.rrDropHistory(dead) {
		return answer, probed, false
	}
	if g.balancer.replaceSlot(dead.member, chosen.Tag()) {
		s.logger.Info("round_robin: ", chosen.Tag(), " replaces ", dead.member, " in its slot")
	}
	return answer, probed, true
}

// rrProbeFirstAnswer probes candidates in parallel batches of batchSize, in config
// order, and returns the first one in config order that answered in the first
// batch with any answer; it does not probe further batches then. The answers of
// every candidate that answered in a batch are written to history, the silence is
// not. byHistory counts fresh history as an answer (rrProbeNode). probed counts
// the candidates actually probed (fresh history is no probe). It stops when the
// check is aborted (ctx cancelled or the self-heal frozen).
func (g *URLTestGroup) rrProbeFirstAnswer(ctx context.Context, candidates []adapter.Outbound, batchSize int, byHistory bool, heal *rrHeal) (rrProbe, adapter.Outbound, int) {
	if batchSize < 1 {
		batchSize = 1
	}
	var probed int
	for start := 0; start < len(candidates); start += batchSize {
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[start:end]
		answers := make([]rrProbe, len(batch))
		var wg sync.WaitGroup
		for i, candidate := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answers[i] = g.rrProbeNode(ctx, candidate, byHistory)
			}()
		}
		wg.Wait()
		if heal.aborted(ctx) {
			return rrProbe{}, nil, probed
		}
		first := -1
		for i, answer := range answers {
			if !answer.fresh {
				probed++
			}
			if !answer.answered {
				continue
			}
			g.rrStore(answer)
			if first < 0 {
				first = i
			}
		}
		if first >= 0 {
			return answers[first], batch[first], probed
		}
	}
	return rrProbe{}, nil, probed
}

// rrCandidates returns the nodes outside the pool in config order; byDelay puts the
// ones with history first, fastest first.
func (g *URLTestGroup) rrCandidates(byDelay bool) []adapter.Outbound {
	inPool := make(map[string]bool)
	for _, tag := range g.balancer.poolTags() {
		inPool[tag] = true
	}
	var withHistory []candidate
	candidates := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if inPool[detour.Tag()] || !common.Contains(detour.Network(), N.NetworkTCP) {
			continue
		}
		if byDelay {
			if history := g.history.LoadURLTestHistory(RealTag(detour, N.NetworkTCP)); history != nil {
				withHistory = append(withHistory, candidate{tag: detour.Tag(), delay: history.Delay, alive: true})
				continue
			}
		}
		candidates = append(candidates, detour)
	}
	if len(withHistory) == 0 {
		return candidates
	}
	sortCandidatesByDelay(withHistory)
	ordered := make([]adapter.Outbound, 0, len(withHistory)+len(candidates))
	for _, c := range withHistory {
		if node, loaded := g.outbound.Outbound(c.tag); loaded {
			ordered = append(ordered, node)
		}
	}
	return append(ordered, candidates...)
}

func rrProbeSummary(probes ...rrProbe) string {
	var summary string
	for i, probe := range probes {
		if probe.member == "" {
			continue
		}
		if i > 0 {
			summary += ", "
		}
		summary += probe.member
		if probe.leaf != probe.member {
			summary += " (" + probe.leaf + ")"
		}
		switch {
		case probe.fresh:
			summary += " alive by fresh history"
		case probe.answered:
			summary += " answered in " + strconv.Itoa(int(probe.delay)) + "ms"
		default:
			summary += " silent"
			if probe.err != nil {
				summary += ": " + probe.err.Error()
			}
		}
	}
	return summary
}

// lx:end SPEC 138
