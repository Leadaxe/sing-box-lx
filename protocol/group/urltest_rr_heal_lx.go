package group

// lx:begin SPEC 138 round_robin self-heal: outcomes, the retry, the check and the replacement
//
// The rules are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/ALGORITHM.md: the
// classes of a failed open (§2), the retry and the replacement (§5), the rounds —
// the check of the pool and the replacement search (§4.2) — and their pause (§4.6).
//
// How it is kept:
//   - rrHeal lives in the balancer; one lock (rrHeal.access) guards the round in
//     flight and the pause, E is an atomic read on the hot path;
//   - a round runs in its own goroutine (rrCheck) on a context the group's Close
//     and an event cancel; only that goroutine touches the rrCheck and the cursor;
//   - every probe takes its place in the queue of the core (urltest_rr_queue_lx.go);
//   - the slots are written by one writer at a time under the flag of the run
//     (URLTestGroup.checking), taken for the write of a slot only (rrWriteSlot).
//
// A connection test (SPEC 139, lxtrace.IsProbe) gets the retry and records nothing;
// a delay measurement through the group (urltest.IsMeasureLx) gets nothing.

import (
	"cmp"
	"context"
	"errors"
	"math"
	"net"
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
	// rrHealGap is the base of the pause of rounds, T (W1).
	rrHealGap = 15 * time.Second
	// rrPauseSteps: the pause doubles up to rrHealGap << rrPauseSteps = 120 s (W2).
	rrPauseSteps = 3
	// rrHealFreeze — the freeze of an event, N3 (T).
	rrHealFreeze = 15 * time.Second
	// rrCandidateBatches is k: batches of candidates per round, the replacement of a
	// check included (L3).
	rrCandidateBatches = 2
)

// rrHeal is the self-heal state of one round_robin group; it lives in the balancer.
type rrHeal struct {
	// access guards the round in flight and the pause of rounds (§4.6).
	access sync.Mutex
	round  *rrRound            // the round in flight (a check or a search); nil for none
	end    *time.Time          // e, the end of the previous round (W1); nil: no pause
	step   atomic.Int32        // g = rrHealGap << step (W2); written under access, read without it by grantExtra
	polled map[string]struct{} // o, the candidates polled since the last ANSWER of a probe of the group, each once
	right  rrRight             // xr of the pause that ends at end (W5)
	// epochEnd is E, the rrClock of the end of the last freeze (N0): while now < E
	// nothing is inferred or done, and an exchange that started before E takes part
	// in no rule. 0: no event yet (epoch reads it as math.MinInt64).
	epochEnd atomic.Int64
	// cursor is c, the position in the config from which the next round goes on
	// with the candidates; only the round in flight moves it.
	cursor int
	// queue is the place of the group in the probe queue of the core (§4.7).
	queue rrQueueOwner
	// lifeAccess guards the budget of the life probes of the group.
	lifeAccess  sync.Mutex
	lifeStarts  [rrLifeBurst]int64
	lifeNext    int
	udpLifeLast int64
	// missAt is the end of the last failed self-heal probe of a leaf: its history
	// written before it is not live (§5).
	missAccess sync.Mutex
	missAt     map[string]time.Time
}

// rrRound is the round in flight.
type rrRound struct {
	cancel context.CancelFunc // aborts it; set once it runs (rrHealRound)
	done   chan struct{}      // closed at its end (yield)
	extra  bool               // the round of the right xr (W5)
}

// rrRight is the state of the right xr of the current pause (W5).
type rrRight uint8

const (
	rrRightNone  rrRight = iota // not given in this pause
	rrRightOpen                 // given, its round not started
	rrRightTaken                // its round started
)

// missed records a self-heal probe of leaf that ended at at with no ANSWER.
func (h *rrHeal) missed(leaf string, at time.Time) {
	h.missAccess.Lock()
	defer h.missAccess.Unlock()
	if h.missAt == nil {
		h.missAt = make(map[string]time.Time)
	}
	if at.After(h.missAt[leaf]) {
		h.missAt[leaf] = at
	}
}

// liveHistory reports live history of leaf (§5): written after the last failed
// self-heal probe of the leaf.
func (h *rrHeal) liveHistory(leaf string, history *adapter.URLTestHistory) bool {
	if history == nil {
		return false
	}
	h.missAccess.Lock()
	at, found := h.missAt[leaf]
	h.missAccess.Unlock()
	return !found || history.Time.After(at)
}

// pause returns g, the current step of the pause of rounds.
func (h *rrHeal) pause() time.Duration {
	h.access.Lock()
	defer h.access.Unlock()
	return rrHealGap << h.step.Load()
}

// pausing reports, at now, whether the pause after the last round still holds.
func (h *rrHeal) pausing(now time.Time) bool {
	h.access.Lock()
	defer h.access.Unlock()
	return h.pausingLocked(now)
}

func (h *rrHeal) pausingLocked(now time.Time) bool {
	return h.end != nil && now.Sub(*h.end) < rrHealGap<<h.step.Load()
}

// lifeHeld is W4: while the step of the pause is above the base and the pause goes
// on, no life probe starts.
func (h *rrHeal) lifeHeld(now time.Time) bool {
	h.access.Lock()
	defer h.access.Unlock()
	return h.step.Load() > 0 && h.pausingLocked(now)
}

// ready reports whether a round could start at now: none in flight, not frozen, the
// pause over or the right of W5 open.
func (h *rrHeal) ready(now time.Time) bool {
	h.access.Lock()
	defer h.access.Unlock()
	return h.readyLocked(now)
}

func (h *rrHeal) readyLocked(now time.Time) bool {
	return h.round == nil && !h.frozen() && (!h.pausingLocked(now) || h.right == rrRightOpen)
}

// extraOpen reports the right xr of the current pause, not spent yet (W5).
func (h *rrHeal) extraOpen() bool {
	h.access.Lock()
	defer h.access.Unlock()
	return h.right == rrRightOpen
}

// beginRound claims the single round of the group (W1: one at a time, after the
// pause, or the round of the right xr).
func (h *rrHeal) beginRound(now time.Time) bool {
	h.access.Lock()
	defer h.access.Unlock()
	if !h.readyLocked(now) {
		return false
	}
	extra := h.pausingLocked(now)
	if extra {
		h.right = rrRightTaken
	}
	h.round = &rrRound{done: make(chan struct{}), extra: extra}
	return true
}

// endRound ends the round in flight. A round that started no probe is no round
// (W1): no pause, the step as it was, and the right xr it took is not spent.
// Otherwise its pause starts (W1) — unless a freeze cut it short (L3: the first
// reason after the freeze gets a round at once). answered: a probe of the round had
// ANSWER; lap: the cursor went round C since the last ANSWER. Without an ANSWER the
// step grows only after a lap (W2); a round of the right xr with no ANSWER keeps the
// step and the end of the pause (W5).
func (h *rrHeal) endRound(now time.Time, probed, answered, lap bool) {
	h.access.Lock()
	defer h.access.Unlock()
	extra := h.round != nil && h.round.extra
	switch {
	case !probed:
		if extra && h.right == rrRightTaken {
			h.right = rrRightOpen
		}
	case h.frozen():
	case !extra || answered:
		if !answered && lap && h.step.Load() < rrPauseSteps {
			h.step.Add(1)
		}
		h.end, h.right = &now, rrRightNone
	}
	if h.round != nil {
		close(h.round.done)
		h.round = nil
	}
}

// running sets the cancel of the round in flight.
func (h *rrHeal) running(cancel context.CancelFunc) {
	h.access.Lock()
	defer h.access.Unlock()
	if h.round != nil {
		h.round.cancel = cancel
	}
}

// probeAnswered is W3: an ANSWER of any probe of the group brings the step back to
// the base and restarts o.
func (h *rrHeal) probeAnswered() {
	h.access.Lock()
	defer h.access.Unlock()
	h.step.Store(0)
	h.polled = nil
}

// poll adds a candidate given to a batch to o, whether the cursor gave it or its
// live history put it first (§4.6).
func (h *rrHeal) poll(tag string) {
	h.access.Lock()
	defer h.access.Unlock()
	if h.polled == nil {
		h.polled = make(map[string]struct{})
	}
	h.polled[tag] = struct{}{}
}

// polls returns |o|.
func (h *rrHeal) polls() int {
	h.access.Lock()
	defer h.access.Unlock()
	return len(h.polled)
}

// polledAll reports whether o holds every one of tags: lap (§4.6).
func (h *rrHeal) polledAll(tags []string) bool {
	h.access.Lock()
	defer h.access.Unlock()
	for _, tag := range tags {
		if _, polled := h.polled[tag]; !polled {
			return false
		}
	}
	return true
}

// grantExtra is W5: an ANSWER of a connection of the group picked during a pause
// above the base gives one round at once, once per pause. A connection picked
// before the pause began ran during the round: its answer is no news of the time
// after it (the pick that started a search gave a second probe of the same
// candidates at once). It reports a new right.
func (h *rrHeal) grantExtra(now, picked time.Time) bool {
	if h.step.Load() == 0 {
		return false // the hot path: the first byte of every watched connection
	}
	h.access.Lock()
	defer h.access.Unlock()
	if h.step.Load() == 0 || !h.pausingLocked(now) || h.right != rrRightNone || picked.Before(*h.end) {
		return false
	}
	h.right = rrRightOpen
	return true
}

// epoch returns E, the end of the last freeze (rrClock; math.MinInt64 for none).
func (h *rrHeal) epoch() int64 {
	if end := h.epochEnd.Load(); end != 0 {
		return end
	}
	return math.MinInt64
}

// freeze is the event of N3: E := now + T, a signal during a freeze extends it
// from its own moment. The round in flight is aborted, the pause is lifted and its
// step goes back to the base (W3, L3); o stays.
func (h *rrHeal) freeze() {
	end := rrClock(time.Now().Add(rrHealFreeze))
	for {
		old := h.epochEnd.Load()
		if old >= end || h.epochEnd.CompareAndSwap(old, end) {
			break
		}
	}
	h.access.Lock()
	h.end, h.right = nil, rrRightNone
	h.step.Store(0)
	h.access.Unlock()
	h.lifeAccess.Lock()
	h.lifeStarts, h.udpLifeLast = [rrLifeBurst]int64{}, 0
	h.lifeAccess.Unlock()
	h.abortCheck()
}

// abortCheck aborts the round in flight.
func (h *rrHeal) abortCheck() {
	h.access.Lock()
	var cancel context.CancelFunc
	if h.round != nil {
		cancel = h.round.cancel
	}
	h.access.Unlock()
	if cancel != nil {
		cancel()
	}
}

// counts reports whether an exchange that started at start goes to the rules: no
// freeze now and not begun before E (N0, I4). A zero start is no exchange's.
func (h *rrHeal) counts(start time.Time) bool {
	return !h.frozen() && (start.IsZero() || rrClock(start) >= h.epoch())
}

// frozen reports whether now < E (N0).
func (h *rrHeal) frozen() bool {
	return rrClock(time.Now()) < h.epochEnd.Load()
}

// aborted reports whether the round running on ctx has to stop: the group closed
// or an event came (N0: the check ends with no verdict).
func (h *rrHeal) aborted(ctx context.Context) bool {
	return ctx.Err() != nil || h.frozen()
}

// yield waits, at most C.TCPTimeout, for the round in flight to end: the full run
// after a network change waits so for the aborted check.
func (h *rrHeal) yield(ctx context.Context) {
	h.access.Lock()
	var done chan struct{}
	if h.round != nil {
		done = h.round.done
	}
	h.access.Unlock()
	if done == nil {
		return
	}
	timer := time.NewTimer(C.TCPTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	case <-timer.C:
	}
}

// claimLife takes a life probe of the node of t at now within the limits (§4.2).
func (h *rrHeal) claimLife(t *rrTrack, now int64, udp bool) bool {
	h.lifeAccess.Lock()
	defer h.lifeAccess.Unlock()
	if t.lifeBusy.Load() {
		return false
	}
	if last := t.lifeLast.Load(); last != 0 && now-last < int64(rrLifeGap) {
		return false
	}
	if udp && h.udpLifeLast != 0 && now-h.udpLifeLast < int64(rrUDPLifeGap) {
		return false
	}
	if oldest := h.lifeStarts[h.lifeNext]; oldest != 0 && now-oldest < int64(rrLifeWindow) {
		return false
	}
	h.lifeStarts[h.lifeNext] = now
	h.lifeNext = (h.lifeNext + 1) % rrLifeBurst
	if udp {
		h.udpLifeLast = now
	}
	t.lifeLast.Store(now)
	t.lifeBusy.Store(true)
	return true
}

// rrNetworkChanged is the N3 event of a network change signal (InterfaceUpdated),
// called before any early return there; it also lifts the K5 marks and the
// quarantine. A no-op outside round_robin.
func (s *URLTest) rrNetworkChanged() {
	if s.balancer != nil {
		s.balancer.heal.freeze()
		s.balancer.rrResetMarks()
	}
}

// rrNetworkRun aborts the check in flight before the full run after a network
// change and waits (bounded) for it to end: it may hold URLTestGroup.checking, and
// the run would then find the flag busy and skip. A no-op outside round_robin.
func (s *URLTest) rrNetworkRun() {
	if s.balancer != nil {
		s.balancer.heal.abortCheck()
		s.balancer.heal.yield(s.group.ctx)
	}
}

// rrWatchWake makes a device or network wake an event (N3). The callback is
// dropped when the group closes.
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

// --- outcomes ----------------------------------------------------------------------

// rrClass is the outcome of a failed open (§2).
type rrClass uint8

const (
	rrCancel      rrClass = iota // CANCEL
	rrLocal                      // LOCAL
	rrRefused                    // REFUSE_DST, explicit
	rrFailActive                 // FAIL_NODE, an active refusal
	rrFailTimeout                // FAIL_NODE, a timeout of the open
)

// rrOutcome classifies a failed open through the pool member member. LOCAL is
// checked before the timeout: some of its errors report Timeout too.
func (s *URLTest) rrOutcome(member adapter.Outbound, err error) rrClass {
	switch {
	case errors.Is(err, context.Canceled):
		return rrCancel
	case s.rrDestinationRefused(member, err):
		return rrRefused
	case rrLocalError(err):
		return rrLocal
	case s.rrSiteOnOpen(member) && rrActiveRefusal(err):
		return rrRefused
	case rrSilence(err):
		return rrFailTimeout
	}
	return rrFailActive
}

// rrDestinationRefused reports whether err of an open through member is its own
// leaf's answer that the destination failed (adapter.DestinationRefusedLx set by
// the leaf itself — a refusal of a detour on the way is the route failing, П).
func (s *URLTest) rrDestinationRefused(member adapter.Outbound, err error) bool {
	var refused *adapter.DestinationRefusedLx
	if !errors.As(err, &refused) {
		return false
	}
	leaf := RealTag(member, N.NetworkTCP)
	return refused.Tag != "" && (refused.Tag == leaf || refused.Tag == member.Tag())
}

// rrSiteOnOpen reports whether the leaf of node goes to the destination itself
// when a connection opens (siteOnOpenLx).
func (s *URLTest) rrSiteOnOpen(node adapter.Outbound) bool {
	return siteOnOpenLx(s.ctx, s.outbound, node)
}

// --- the retry ---------------------------------------------------------------------

// rrHealDial handles a failed dial of a round_robin group: the outcome goes to its
// rules, and the connection of the retry is returned. ok is false when there is no
// retry or it failed too: the caller returns dialErr.
func (s *URLTest) rrHealDial(ctx context.Context, network string, destination M.Socksaddr, failed adapter.Outbound, dialErr error, picked rrPick) (net.Conn, bool) {
	struck := picked.dial.failed()
	switch N.NetworkName(network) {
	case N.NetworkTCP, N.NetworkUDP:
	default:
		return nil, false
	}
	var conn net.Conn
	retry, ok := s.rrHealRetry(ctx, network, failed, dialErr, picked, struck, func(ctx context.Context, dialer adapter.Outbound) error {
		var err error
		conn, err = dialer.DialContext(ctx, network, destination)
		return err
	})
	if !ok {
		return nil, false
	}
	picked.dial, picked.start = retry.dial, retry.start
	return s.rrWatch(ctx, network, retry.dialer, conn, picked, retry.sign, true), true
}

// rrRetried is an opened retry: the chain leaf it went through, the Q1 sign of the
// first node its first byte completes, its tracked dial and its start.
type rrRetried struct {
	dialer adapter.Outbound
	sign   *rrSign
	dial   *rrDial
	start  time.Time
}

// rrHealRetry is the part of a failed open shared by TCP and UDP (rrHealDial,
// rrHealListen): the outcome goes to its rules and the retry opens through the next
// usable slot with open on its chain leaf (§5). struck is a dial D1 on its node
// cancelled: it goes to the retry and records nothing. A UDP failure shifts no key
// and is not counted.
func (s *URLTest) rrHealRetry(ctx context.Context, network string, failed adapter.Outbound, dialErr error, picked rrPick, struck bool, open func(ctx context.Context, dialer adapter.Outbound) error) (rrRetried, bool) {
	if urltest.IsMeasureLx(ctx) {
		return rrRetried{}, false // a delay measurement shows the node as it is
	}
	b := s.balancer
	b.rrClockLook(time.Now()) // N3 before the rules
	if errors.Is(ctx.Err(), context.Canceled) || !struck && errors.Is(dialErr, context.Canceled) {
		return rrRetried{}, false // CANCEL: nothing follows
	}
	tcp := N.NetworkName(network) == N.NetworkTCP
	if !tcp {
		picked.key = rrKey{}
	}
	slots := b.slotsSnapshot()
	failedSlot := slotIndex(slots, failed.Tag())
	// failed may be the chain hop of the node; the rules need the pool member.
	member, loaded := s.outbound.Outbound(failed.Tag())
	if !loaded || failedSlot < 0 && !struck {
		return rrRetried{}, false // a fallback pick outside the pool (cold start)
	}
	class := rrFailActive
	var sign *rrSign
	if !struck {
		class = s.rrOutcome(member, dialErr)
		if !lxtrace.IsProbe(ctx) && b.heal.counts(picked.start) { // N0, I4: the retry only
			if tcp {
				b.rrCountConn(member.Tag(), picked.start)
			}
			sign = s.rrOpenFailed(member, picked, class, tcp, false)
		}
	}
	if (class == rrRefused || class == rrLocal) && picked.key.valid && b.memory.shifts.marked(picked.key.table) {
		return rrRetried{}, false // K5: a marked key keeps the retry of FAIL_NODE only
	}
	// A nested urltest retries on its own (SPEC 054, and a round_robin one by §5: its
	// open carries no mark of this group, rrDialStart): two attempts per call at most.
	// Otherwise only the outermost round_robin group of the chain of one open retries
	// (§5): an inner one, found by the mark of the context, leaves it to the outer.
	if rrNestedURLTest(member) || picked.inner || ctx.Err() != nil {
		return rrRetried{}, false
	}
	retry := s.rrRetryTarget(slots, failedSlot, failed.Tag(), network)
	if retry == nil {
		return rrRetried{}, false
	}
	s.group.balancedRetrying(ctx, retry, failed.Tag())
	leafCtx, dialer, err := adapter.ResolveChainLeaf(ctx, retry)
	if err != nil {
		return rrRetried{}, false
	}
	retryStart := time.Now()
	retryCtx, dial := s.rrTrackDial(rrMarkDial(leafCtx), b.trackOf(retry.Tag()), retry, dialer, rrClock(retryStart), !tcp)
	if err = open(retryCtx, dialer); err != nil {
		s.logger.DebugContext(ctx, "round_robin retry via ", retry.Tag(), " failed: ", err)
		if !dial.failed() && ctx.Err() == nil && !lxtrace.IsProbe(ctx) && b.heal.counts(retryStart) {
			if class := s.rrOutcome(retry, err); class != rrCancel && b.trackOf(retry.Tag()) != nil {
				if tcp {
					b.rrCountConn(retry.Tag(), retryStart)
				}
				s.rrOpenFailed(retry, rrPick{start: retryStart}, class, tcp, true)
			}
		}
		return rrRetried{}, false
	}
	dial.opened()
	s.group.balancedRetried(ctx, retry) // only an opened retry moves the connection's node
	s.logger.DebugContext(ctx, "round_robin: ", failed.Tag(), " failed (", network, "), retried via ", retry.Tag())
	return rrRetried{dialer: dialer, sign: sign, dial: dial, start: retryStart}, true
}

// rrRetryTarget returns the node of the retry (§5): going round from the slot after
// the failed one (from the first one when the failed node was struck and replaced
// meanwhile), a slot that is live, takes network and holds no nested urltest — the
// first one under no suspicion, else the first one under suspicion: the suspicion is
// as soft for the retry as for the pick (§4.3).
func (s *URLTest) rrRetryTarget(slots []slot, failedSlot int, failed string, network string) adapter.Outbound {
	b := s.balancer
	now, epoch := rrClock(time.Now()), b.heal.epoch()
	n := len(slots)
	var suspected adapter.Outbound
	for step := 1; step <= n; step++ {
		entry := slots[(failedSlot+step+n)%n]
		if failedSlot < 0 {
			entry = slots[step-1]
		}
		if entry.tag == failed || !s.slotAlive(entry.tag) || !s.slotSupports(entry.tag, network) {
			continue
		}
		node, loaded := s.outbound.Outbound(entry.tag)
		if !loaded || rrNestedURLTest(node) {
			continue
		}
		if entry.track == nil || !entry.track.suspect(now, epoch) {
			return node
		}
		if suspected == nil {
			suspected = node
		}
	}
	return suspected
}

// rrOpenFailed applies the rules to a failed open: S1, S2, K1, K2/K4 and the Q1
// sign. It returns the sign the first byte of the retry completes. A failed open of
// the retry (retry) is a sign of its node with no key shift and no K2 memory (§4.4:
// the slot of the retry) and no Q1 sign (it has no retry of its own).
func (s *URLTest) rrOpenFailed(member adapter.Outbound, picked rrPick, class rrClass, tcp, retry bool) *rrSign {
	t := s.balancer.trackOf(member.Tag())
	if t == nil {
		return nil
	}
	a, now := rrClock(picked.start), rrClock(time.Now())
	eligible := tcp && !picked.detour && !retry
	switch class {
	case rrLocal:
		if !s.rrSiteOnOpen(member) {
			s.rrLifeProbe(member, t, a, false) // S1: LOCAL on the open to the proxy node's address
		}
		return nil
	case rrRefused:
		var sign *rrSign
		if eligible && picked.key.valid && !s.balancer.memory.shifts.marked(picked.key.table) {
			s.rrShiftOn(picked.key, picked.shift, member.Tag(), rrShiftK1) // K1: the key moves at once
			sign = &rrSign{s: s, node: member.Tag(), key: picked.key, b: now, need: rrSignRetry}
		}
		s.rrLifeProbe(member, t, a, false) // S1, K1
		return sign
	case rrFailActive:
		s.rrSoftSign(picked.key, picked.shift, member.Tag(), a, now, false, eligible)
		var sign *rrSign
		if !retry {
			sign = s.rrFailSign(t, picked, member.Tag(), now, tcp) // Q1, before the probe whose answer settles it
		}
		s.rrLifeProbe(member, t, a, false) // S1
		return sign
	default:
		if tcp {
			t.suspectAt(a, now) // S2: the bypass at once
		}
		s.rrSoftSign(picked.key, picked.shift, member.Tag(), a, now, false, eligible)
		var sign *rrSign
		if !retry && s.rrLiveOthers(member.Tag(), a, now) {
			sign = s.rrFailSign(t, picked, member.Tag(), now, tcp) // Q1: Live(X; P \ {n})
		}
		s.rrLifeProbe(member, t, a, !tcp) // S2; a UDP open makes no TCP suspicion (V9)
		return sign
	}
}

// rrFailSign records the Q1 sign of FAIL_NODE on the node of t: it counts once the
// retry answered and an ANSWER via the node after it came.
func (s *URLTest) rrFailSign(t *rrTrack, picked rrPick, node string, at int64, tcp bool) *rrSign {
	if !tcp {
		return nil
	}
	sign := &rrSign{s: s, node: node, key: picked.key, b: at, need: rrSignProof | rrSignRetry}
	t.addPending(sign)
	return sign
}

// rrLiveOthers reports Live(X; P \ {node}) for an exchange sent at w, at now.
func (s *URLTest) rrLiveOthers(node string, w, now int64) bool {
	for _, entry := range s.balancer.slotsSnapshot() {
		if entry.tag != node && entry.track != nil && entry.track.live(w, rrHalf(w, now)) {
			return true
		}
	}
	return false
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

// --- probes ------------------------------------------------------------------------

// rrProbe is the outcome of one probe.
type rrProbe struct {
	member   string // the pool member (or candidate) tag
	leaf     string // the tag its history lives under (RealTag)
	answered bool
	delay    uint16
	err      error
	started  time.Time
	ended    time.Time
}

// cancelled reports a probe cut short by its context (the round, the group, the
// askers of a shared flight gone): no outcome for any rule (U3) — neither an
// ANSWER nor its absence.
func (p rrProbe) cancelled() bool {
	return !p.answered && errors.Is(p.err, context.Canceled)
}

// rrProbeTimeoutNs is T, the timeout of a probe (C.TCPTimeout); atomic so that
// tests can shorten it while probes of other tests still run.
var rrProbeTimeoutNs atomic.Int64

func init() {
	rrProbeTimeoutNs.Store(int64(C.TCPTimeout))
}

func rrProbeTimeout() time.Duration {
	return time.Duration(rrProbeTimeoutNs.Load())
}

// rrProbeNode probes the live leaf of member with the group's URL; it writes no
// history.
func (g *URLTestGroup) rrProbeNode(ctx context.Context, member adapter.Outbound) (result rrProbe) {
	result = rrProbe{member: member.Tag(), leaf: RealTag(member, N.NetworkTCP), started: time.Now()}
	defer func() {
		result.ended = time.Now()
		if g.balancer != nil {
			g.balancer.rrClockLook(result.ended) // N3: the clocks are compared at every outcome
		}
	}()
	if result.leaf == "" {
		result.err = E.New("no selected node")
		return result
	}
	leaf, loaded := g.outbound.Outbound(result.leaf)
	if !loaded {
		result.err = E.New("outbound not found: ", result.leaf)
		return result
	}
	testCtx, cancel := context.WithTimeout(ctx, rrProbeTimeout())
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

// rrStore writes the answer of a probe to history (D3).
func (g *URLTestGroup) rrStore(probe rrProbe) {
	if probe.answered {
		g.history.StoreURLTestHistory(probe.leaf, &adapter.URLTestHistory{Time: time.Now(), Delay: probe.delay})
	}
}

// --- rounds (§4.2, §5) ------------------------------------------------------------

// rrStartRound starts round (rrHealCheck or rrHealSearch) when the round of the
// group is free and the pause is over (or the right xr of W5 is open); it reports
// whether it did. Only a soft sign on a pool node asks for the check (§4.2).
func (s *URLTest) rrStartRound(round func()) bool {
	if !s.balancer.heal.beginRound(time.Now()) {
		return false
	}
	go round()
	return true
}

// rrStartSearch starts the replacement search (§5): an empty slot or the slot of a
// quarantined node with no replacement; no probe of a pool node, no control. The
// slot of an unverified node is no slot of the search: the check verifies it. With
// no candidate, or while a run holds its flag (the run fills the slots itself),
// there is nothing to probe and no round (W1).
func (s *URLTest) rrStartSearch() bool {
	return !s.group.checking.Load() && s.rrHasCandidate() && s.rrStartRound(s.rrHealSearch)
}

// rrExtraRound is the round of the right xr (W5): the check when a suspicion still
// asks for it (D4), else the search when a slot is empty; with no reason the right
// waits for one until the end of the pause.
func (s *URLTest) rrExtraRound() {
	now, epoch := rrClock(time.Now()), s.balancer.heal.epoch()
	for _, entry := range s.balancer.slotsSnapshot() {
		if entry.track != nil && entry.track.wantCheck.Load() && entry.track.suspected(now, epoch) {
			s.rrStartRound(s.rrHealCheck)
			return
		}
	}
	if s.rrSlotToVerify() {
		s.rrStartRound(s.rrHealCheck)
	} else if s.rrSlotToFill() {
		s.rrStartSearch()
	}
}

// rrSlotToFill reports a slot to fill (§5): empty (D1), with a quarantined node, or
// with a node without history that is not unverified.
func (s *URLTest) rrSlotToFill() bool {
	for _, entry := range s.balancer.slotsSnapshot() {
		if s.rrSlotFillable(entry.tag) {
			return true
		}
	}
	return false
}

// rrPoolOthers reports a slot of the pool other than that of tag that is not empty
// (Q4: "the rest of the pool"); the slot of an unverified node, with no history, is
// not of the rest.
func (s *URLTest) rrPoolOthers(tag string) bool {
	for _, entry := range s.balancer.slotsSnapshot() {
		if entry.tag != tag && !s.rrSlotEmpty(entry.tag) {
			return true
		}
	}
	return false
}

// rrSlotEmpty reports a slot the pick skips and the search fills (§5): no node, a
// quarantined one, or one without history (a dead occupant the run left, a leaf
// whose shared history D1 of another group erased). It is no slot of the pool for
// the check (I9), except the slot of an unverified node, which the search does not
// fill either (rrSlotFillable).
func (s *URLTest) rrSlotEmpty(tag string) bool {
	return tag == "" || !s.slotAlive(tag)
}

type rrCheckRole uint8

const (
	rrRoleFirst     rrCheckRole = iota // the first probe of a pool node (step 1)
	rrRoleCandidate                    // a candidate of a batch (step 2, §5)
	rrRoleDecisive                     // the decisive probe of a pool node (step 3)
	rrRoleAfter                        // the control after a part (step 4)
)

type rrCheckResult struct {
	role  rrCheckRole
	index int // the pool node; the candidate for rrRoleCandidate; the part for rrRoleAfter
	batch int
	probe rrProbe
}

// rrDecisivePart is one part of the decisive probes (step 4): up to U − 1 nodes
// and the place of its control after.
type rrDecisivePart struct {
	open    int  // decisive probes not ended yet (or not started)
	missed  bool // a decisive probe had no ANSWER
	afterOK bool // its control after answered
}

// rrCheck is one round in flight — a check, or a search (refill from the start);
// only its own goroutine touches it.
type rrCheck struct {
	s          *URLTest
	ctx        context.Context
	queue      *rrQueue
	x          time.Time // the start of the round
	pool       []adapter.Outbound
	size       int // |P|: the size of a batch of candidates
	results    chan rrCheckResult
	flying     int
	nodes      []rrPoolProbe // the probes of each pool node, by index
	waiting    []int         // pool nodes of a part whose decisive probe waits (step 3)
	parts      []*rrDecisivePart
	control    adapter.Outbound
	b0         time.Time
	candidates []adapter.Outbound // the candidates probed, by index
	ready      []adapter.Outbound // the candidates that answered, in order
	seen       map[string]bool
	batches    int // the batches of candidates of the round
	left       int // the probes of the latest batch in flight
	refill     bool
	draining   bool // no new batch: the probes in flight end the round
	anyAnswer  bool // a probe of the round answered (W2)
	// grants carries the work of the places granted by the queue to the loop: the
	// loop never waits for places itself, since the places it waits for may be
	// held by its own part whose probes wait for the loop. Its buffer holds every
	// request of the round (the first probes, the decisive parts and k batches of
	// candidates, each in parts of at most U), so the queue hands a grant over
	// without waiting. A request not granted yet counts as flying; asked are the
	// requests of the round.
	grants chan func()
	asked  []*rrQueueRequest
	// reserved are the places of granted decisive parts not used yet: by a decisive
	// probe waiting to start or by a control after.
	reserved int
	launched int // the probes the round started; none: no round (W1)
}

// rrPoolProbe is what the check knows of the probes of one pool node.
type rrPoolProbe struct {
	answered   bool      // a probe of the node answered in this check (D3)
	firstAt    time.Time // the start of its first probe
	firstEnded bool
	voided     bool // its decisive probe was cancelled: no D1 (U3)
	part       int  // its decisive part
}

// request asks the queue for n places and hands then to the loop once they are
// granted (U1). The queue hands the grant over under its lock; the buffer of grants
// holds every request of the round, so the hand-over never waits.
func (c *rrCheck) request(n int, then func()) {
	c.flying++
	c.asked = append(c.asked, c.queue.ask(&c.s.balancer.heal.queue, n, func() { c.grants <- then }))
}

// settle ends a round: the requests still waiting are withdrawn, and the places
// granted meanwhile go back through probes that end at once.
func (c *rrCheck) settle() {
	for _, request := range c.asked {
		c.queue.withdraw(&c.s.balancer.heal.queue, request)
	}
	c.asked = nil
	for {
		select {
		case then := <-c.grants:
			then()
		default:
			c.queue.release(c.reserved)
			c.reserved, c.waiting = 0, nil
			return
		}
	}
}

// launch starts one probe on a place the round holds; the place goes back at its
// end. batch is the batch of candidates the probe belongs to (0: the first probes
// of the pool; -1: none), fixed when its places were asked for.
func (c *rrCheck) launch(role rrCheckRole, index int, node adapter.Outbound, from time.Time, batch int) {
	c.flying++
	c.launched++
	// The decisive probe of a node whose first probe still flies flies beside it from
	// the moment of step 3, not after its end (U3).
	alone := role == rrRoleDecisive && !c.nodes[index].firstEnded
	go func() {
		probe := c.s.group.rrSelfProbe(c.ctx, node, from, alone)
		c.queue.release(1)
		c.results <- rrCheckResult{role, index, batch, probe}
	}()
}

// launchAll asks for the probes of nodes in parts of at most U (U1); each part
// starts when its places are granted. A part keeps the batch it was asked for in: a
// first probe of a pool larger than U granted after a batch of candidates started
// does not count against that batch (c.left).
func (c *rrCheck) launchAll(role rrCheckRole, first int, nodes []adapter.Outbound) {
	batch := c.batches
	for start := 0; start < len(nodes); start += rrQueueCap {
		end := min(start+rrQueueCap, len(nodes))
		c.request(end-start, func() {
			for i := start; i < end; i++ {
				if role == rrRoleFirst {
					c.nodes[first+i].firstAt = time.Now()
				}
				c.launch(role, first+i, nodes[i], c.x, batch)
			}
		})
	}
}

// polling reports whether the candidates are still needed: no control yet, or no
// candidate ready for the replacement.
func (c *rrCheck) polling() bool {
	if c.draining {
		return false
	}
	if c.refill {
		return len(c.ready) == 0
	}
	return c.control == nil
}

// nextBatch starts the next batch of candidates, at most k per round.
func (c *rrCheck) nextBatch() bool {
	if c.draining || c.batches == rrCandidateBatches {
		return false
	}
	batch := c.s.rrCandidateBatch(c.seen, c.size)
	if len(batch) == 0 {
		return false
	}
	c.batches++
	c.left = len(batch)
	first := len(c.candidates)
	c.candidates = append(c.candidates, batch...)
	c.launchAll(rrRoleCandidate, first, batch)
	return true
}

// loop takes the outcomes until nothing is in flight or done holds. A new batch of
// candidates starts H after the previous one, or at once when the previous one
// ended with no answer (step 2). A decisive probe waiting for its first probe
// starts when that one ended with no answer or flew H (step 3).
func (c *rrCheck) loop(done func() bool) {
	timer := time.NewTimer(rrHangAfter)
	defer timer.Stop()
	tick := time.NewTicker(rrHangAfter / 10)
	defer tick.Stop()
	for c.flying > 0 && !done() {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
			if c.polling() {
				c.nextBatch()
			}
			timer.Reset(rrHangAfter)
		case <-tick.C:
			if len(c.waiting) > 0 {
				c.startDecisive()
			}
		case then := <-c.grants:
			c.flying--
			then()
		case r := <-c.results:
			c.flying--
			c.take(r)
			if (r.role == rrRoleFirst || r.role == rrRoleCandidate) && r.batch == c.batches {
				c.left--
				if c.left == 0 && c.polling() && c.nextBatch() {
					timer.Reset(rrHangAfter)
				}
			}
		}
	}
}

func (c *rrCheck) take(r rrCheckResult) {
	if r.probe.answered {
		c.anyAnswer = true
	}
	if r.probe.cancelled() {
		c.void(r) // U3: a cancelled probe is no outcome
		return
	}
	switch r.role {
	case rrRoleFirst:
		c.nodes[r.index].firstEnded = true
		if r.probe.answered {
			if !c.nodes[r.index].answered {
				c.nodes[r.index].answered = true
				c.s.rrAlive(r.probe) // D3
			}
			c.found(c.pool[r.index])
		}
		if len(c.waiting) > 0 {
			c.startDecisive()
		}
	case rrRoleDecisive:
		part := c.parts[c.nodes[r.index].part]
		if r.probe.answered {
			if !c.nodes[r.index].answered {
				c.nodes[r.index].answered = true
				c.s.rrAlive(r.probe) // D3
			}
		} else {
			part.missed = true
		}
		c.partDone(c.nodes[r.index].part)
	case rrRoleAfter:
		if r.probe.answered {
			c.parts[r.index].afterOK = true
			c.s.rrAlive(r.probe) // D3 of the control
		}
	case rrRoleCandidate:
		if r.probe.answered {
			c.s.rrAlive(r.probe) // D3
			node := c.candidates[r.index]
			c.ready = append(c.ready, node)
			c.found(node)
		}
	}
}

// void takes a cancelled probe: it ends its role and proves nothing either way.
func (c *rrCheck) void(r rrCheckResult) {
	switch r.role {
	case rrRoleFirst:
		c.nodes[r.index].firstEnded = true
		if len(c.waiting) > 0 {
			c.startDecisive()
		}
	case rrRoleDecisive:
		c.nodes[r.index].voided = true
		c.partDone(c.nodes[r.index].part)
	}
}

// found is an ANSWER in the check: the first one is the control (b0). The pool
// nodes that have not answered get their decisive probes in parts of up to U − 1
// nodes, each part with the place of its control after (step 3, 4).
func (c *rrCheck) found(node adapter.Outbound) {
	if c.control != nil || c.refill {
		return
	}
	c.control, c.b0 = node, time.Now()
	var nodes []int
	for i := range c.pool {
		if !c.nodes[i].answered {
			nodes = append(nodes, i)
		}
	}
	for start := 0; start < len(nodes); start += rrQueueCap - 1 {
		end := min(start+rrQueueCap-1, len(nodes))
		part := len(c.parts)
		c.parts = append(c.parts, &rrDecisivePart{open: end - start})
		for _, i := range nodes[start:end] {
			c.nodes[i].part = part
		}
		members := nodes[start:end]
		c.request(end-start+1, func() {
			c.reserved += len(members) + 1
			c.waiting = append(c.waiting, members...)
			c.startDecisive()
		})
	}
}

// startDecisive starts the decisive probes that may start now (step 3): the first
// probe ended with no ANSWER or flew H; a node that answered meanwhile gets none
// and gives its place back.
func (c *rrCheck) startDecisive() {
	now := time.Now()
	kept := c.waiting[:0]
	for _, i := range c.waiting {
		switch {
		case c.nodes[i].answered:
			c.reserved--
			c.queue.release(1)
			c.partDone(c.nodes[i].part)
		case c.nodes[i].firstEnded || !c.nodes[i].firstAt.IsZero() && now.Sub(c.nodes[i].firstAt) >= rrHangAfter:
			c.reserved--
			c.launch(rrRoleDecisive, i, c.pool[i], c.b0, -1)
		default:
			kept = append(kept, i)
		}
	}
	c.waiting = kept
}

// partDone takes the end of one decisive probe of part: when the last one ended
// (y) and one of them had no ANSWER, the control after starts through the control,
// not earlier than y; otherwise its place goes back.
func (c *rrCheck) partDone(index int) {
	part := c.parts[index]
	part.open--
	if part.open > 0 {
		return
	}
	c.reserved--
	if part.missed {
		c.launch(rrRoleAfter, index, c.control, time.Now(), -1)
	} else {
		c.queue.release(1)
	}
}

// drain lets the probes in flight end with no new batch: their answers are D3 and
// W3, and the round ends when the last one did.
func (c *rrCheck) drain() {
	c.draining = true
	c.loop(func() bool { return false })
}

// takeReady returns the next candidate that answered and still is one (rrCandidate).
func (c *rrCheck) takeReady() adapter.Outbound {
	for len(c.ready) > 0 {
		node := c.ready[0]
		c.ready = c.ready[1:]
		if c.s.rrCandidate(node) {
			return node
		}
	}
	return nil
}

// rrHealRound runs one round on a context the group's Close and an event (N0)
// cancel, and ends it with its pause (W1, W2).
func (s *URLTest) rrHealRound(round func(c *rrCheck)) {
	heal := &s.balancer.heal
	ctx, cancel := context.WithCancel(s.group.ctx)
	heal.running(cancel)
	slots := s.balancer.slotsSnapshot()
	c := &rrCheck{s: s, ctx: ctx, queue: s.group.rrQueue(), x: time.Now(), size: len(slots), seen: make(map[string]bool)}
	defer func() {
		cancel()
		c.settle()
		heal.endRound(time.Now(), c.launched > 0, c.anyAnswer, s.rrLap())
	}()
	if heal.aborted(ctx) {
		return
	}
	round(c)
}

// rrHealCheck is the check of the pool (§4.2) followed by D1 and the replacement
// (§5). It owns the round taken by beginRound.
func (s *URLTest) rrHealCheck() {
	s.rrHealRound(func(c *rrCheck) {
		for _, entry := range s.balancer.slotsSnapshot() {
			if s.rrSlotEmpty(entry.tag) && !s.group.rrUnverified(entry.tag) {
				continue // a quarantined node or one without history takes no part; a struck one left its slot (§4.2); an unverified one does (SPEC 019)
			}
			if node, loaded := s.outbound.Outbound(entry.tag); loaded && common.Contains(node.Network(), N.NetworkTCP) {
				c.pool = append(c.pool, node)
				c.seen[node.Tag()] = true
			}
		}
		n := len(c.pool)
		c.results = make(chan rrCheckResult, 3*n+2*rrCandidateBatches*c.size+1)
		c.grants = make(chan func(), 2*n+2*rrCandidateBatches*c.size+1)
		c.nodes = make([]rrPoolProbe, n)
		c.launchAll(rrRoleFirst, 0, c.pool) // step 1
		c.left = n
		if n == 0 {
			c.nextBatch()
		}
		c.loop(func() bool { return false })
		if c.s.balancer.heal.aborted(c.ctx) {
			return // N0: no verdict
		}
		if c.control == nil {
			s.logger.Debug("round_robin: no probe answered the check; no verdict") // D5
		}
		s.rrStrike(c.ctx, c)
		s.rrRefill(c.ctx, c)
		c.drain()
	})
}

// rrHealSearch is the replacement search (§5): batches of candidates of the pool
// size, at most k; a candidate that answered its own probe takes the slot.
func (s *URLTest) rrHealSearch() {
	s.rrHealRound(func(c *rrCheck) {
		if s.group.checking.Load() {
			return // a run is in flight: the run fills the slots itself (§5)
		}
		for _, entry := range s.balancer.slotsSnapshot() {
			if entry.tag != "" {
				c.seen[entry.tag] = true
			}
		}
		c.results = make(chan rrCheckResult, rrCandidateBatches*c.size+1)
		c.grants = make(chan func(), rrCandidateBatches*c.size+1)
		s.rrRefill(c.ctx, c)
		c.drain()
	})
}

// rrLap is lap (§4.6): every candidate of C was polled since the last ANSWER of a
// probe of the group — by the cursor or first by its live history — or C holds none.
func (s *URLTest) rrLap() bool {
	var candidates []string
	for _, node := range s.group.outbounds {
		if s.rrCandidate(node) {
			candidates = append(candidates, node.Tag())
		}
	}
	return s.balancer.heal.polledAll(candidates)
}

// rrStrike is D1 for every pool node whose decisive probe had no ANSWER while the
// control after its part answered, read at the verdict: no ANSWER via the node
// that started at or after x (proved), no history written after x. The node
// leaves its slot at once, the slot is empty even with no replacement (R1), and a
// second D1 since the scheduled run is the quarantine of Q4.
func (s *URLTest) rrStrike(ctx context.Context, c *rrCheck) {
	b := s.balancer
	for i, node := range c.pool {
		if c.nodes[i].answered || c.nodes[i].voided || c.control == nil || b.heal.aborted(ctx) {
			continue // D3, no outcome of the decisive probe (U3), or no control (D5)
		}
		if part := c.nodes[i].part; part >= len(c.parts) || !c.parts[part].afterOK {
			continue // D4
		}
		t := b.trackOf(node.Tag())
		if t == nil || t.proved.Load() >= rrClock(c.x) {
			continue
		}
		if !s.group.history.DeleteURLTestHistoryNotAfterLx(RealTag(node, N.NetworkTCP), c.x) {
			continue // D1: a scheduled run answered for the node since x, no verdict
		}
		others := s.rrPoolOthers(node.Tag())
		b.unverified.unmark(node.Tag())
		s.rrDead(t)             // before the slot empties: a dial in flight either still finds its slot or is marked struck (its retry)
		b.clearSlot(node.Tag()) // R1
		quarantined := b.rrStrikeOut(node.Tag(), others)
		s.logger.Info("round_robin: ", node.Tag(), " struck out as dead; control ", c.control.Tag())
		if quarantined {
			s.logger.Info("round_robin: ", node.Tag(), " quarantined: struck out twice since the scheduled run")
		}
	}
}

// rrRefill puts into every empty slot and every slot of a quarantined node a
// candidate that answered its probe in this round, else one of the batches the
// round left of its k (§5); with no answer the slot stays as it is. The flag of the
// run is taken for each write only: while a run holds it, the run fills the slots
// itself and the round writes nothing.
func (s *URLTest) rrRefill(ctx context.Context, c *rrCheck) {
	b := s.balancer
	var empty []string
	for _, entry := range b.slotsSnapshot() {
		if s.rrSlotFillable(entry.tag) {
			empty = append(empty, entry.tag)
		}
	}
	if len(empty) == 0 {
		return // nothing to replace
	}
	c.refill, c.left = true, 0 // L3: the batches of the check count against its k
	for _, dead := range empty {
		for {
			if b.heal.aborted(ctx) {
				return
			}
			node := c.takeReady()
			if node == nil {
				if c.flying == 0 && !c.nextBatch() {
					return // the round is over
				}
				c.loop(func() bool { return len(c.ready) > 0 })
				if c.ctx.Err() != nil {
					return
				}
				continue
			}
			if dead != "" && !s.rrSlotFillable(dead) {
				break // its node got history back meanwhile (or a manual test left it unverified): the slot is no longer to fill
			}
			replaced, held := s.rrWriteSlot(ctx, func() bool { return b.replaceSlot(dead, node.Tag()) })
			if !held {
				return // a run holds the flag (the run fills the slots itself), or the round is over
			}
			if replaced {
				s.logger.Info("round_robin: ", node.Tag(), " takes the slot of ", cmp.Or(dead, "a struck node"))
				break
			}
		}
	}
}

// rrLiveHistory returns the history of the leaf of node when it is live (§5), else
// nil.
func (s *URLTest) rrLiveHistory(node adapter.Outbound) *adapter.URLTestHistory {
	leaf := RealTag(node, N.NetworkTCP)
	if history := s.group.history.LoadURLTestHistory(leaf); s.balancer.heal.liveHistory(leaf, history) {
		return history
	}
	return nil
}

// rrCandidateBatch returns the next batch of at most size candidates not yet probed
// in this round (§5): those with live history first, fastest first, then the rest
// from the cursor going round in config order; the cursor moves past the last one
// it gave (I8), and o takes every one given, either way (§4.6).
func (s *URLTest) rrCandidateBatch(seen map[string]bool, size int) []adapter.Outbound {
	g, b := s.group, s.balancer
	usable := func(node adapter.Outbound) bool {
		return !seen[node.Tag()] && s.rrCandidate(node)
	}
	var alive []candidate
	byTag := make(map[string]adapter.Outbound)
	for _, node := range g.outbounds {
		if !usable(node) {
			continue
		}
		if history := s.rrLiveHistory(node); history != nil {
			alive = append(alive, candidate{tag: node.Tag(), delay: history.Delay, alive: true})
			byTag[node.Tag()] = node
		}
	}
	sortCandidatesByDelay(alive)
	batch := make([]adapter.Outbound, 0, size)
	for _, entry := range alive {
		if len(batch) == size {
			return batch
		}
		batch = append(batch, byTag[entry.tag])
		seen[entry.tag] = true
		b.heal.poll(entry.tag) // o: polled first by its live history, the cursor stays (I8)
	}
	n := len(g.outbounds)
	from := b.heal.cursor // I8: the walk goes from where the cursor stood; it moves past each one given
	for step := 0; step < n && len(batch) < size; step++ {
		i := (from + step) % n
		node := g.outbounds[i]
		if !usable(node) || byTag[node.Tag()] != nil {
			continue
		}
		batch = append(batch, node)
		seen[node.Tag()] = true
		b.heal.cursor = (i + 1) % n
		b.heal.poll(node.Tag())
	}
	return batch
}

// rrCandidate reports whether node is a candidate of C (§5): out of the pool, not
// quarantined, taking TCP.
func (s *URLTest) rrCandidate(node adapter.Outbound) bool {
	b, tag := s.balancer, node.Tag()
	return !b.isQuarantined(tag) && !b.inPool(tag) && common.Contains(node.Network(), N.NetworkTCP)
}

// rrHasCandidate reports whether C holds a candidate to probe.
func (s *URLTest) rrHasCandidate() bool {
	for _, node := range s.group.outbounds {
		if s.rrCandidate(node) {
			return true
		}
	}
	return false
}

// rrWriteSlot writes a slot for the self-heal under URLTestGroup.checking, taken for
// the write only (§5); held is false, with nothing written, when the flag is busy (a
// run does the same work) or the round has been aborted.
func (s *URLTest) rrWriteSlot(ctx context.Context, write func() bool) (written, held bool) {
	if !s.group.checking.CompareAndSwap(false, true) {
		return false, false
	}
	defer s.group.checking.Store(false)
	if s.balancer.heal.aborted(ctx) {
		return false, false
	}
	return write(), true
}

// lx:end SPEC 138
