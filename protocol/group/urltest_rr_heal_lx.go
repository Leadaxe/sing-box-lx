package group

// lx:begin SPEC 138 round_robin self-heal: outcomes, the retry, the check and the replacement
//
// The rules named here are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/
// ALGORITHM.md.
//
// A failed open of a round_robin connection is classified (§2): CANCEL (the client
// or the group gave up: nothing follows), LOCAL, an explicit REFUSE_DST (a code of
// the node's own leaf; a refused or reset open of a leaf that goes to the site
// itself), FAIL_NODE by an active refusal or by a timeout. It is retried once
// through another slot of the pool whose leaf is no nested urltest, the next one
// with no suspicion first, else the next one under suspicion (§5); a key marked by
// K5 keeps only the retry of FAIL_NODE. In a freeze (N0) the retry stays and nothing
// else: the open and the retry began before E and go to no rule. The sign then
// goes to its rules: S1 (a fast refusal: the life probe, the bypass only after it
// failed), S2 (a timeout: the bypass and the life probe at once), K1 (the key
// shifts at once), K2/K4 (the memory of a soft sign) and Q1 (the count).
//
// A round (§4.2) is a check of the pool or a replacement search; one at a time per
// group, the next one not earlier than the pause after the previous one (§4.6): 15 s,
// doubled up to 120 s after a round with no ANSWER at a lap of the cursor, back to
// 15 s at the ANSWER of any probe of the group and at N3; the first byte of a
// connection in a pause above the base gives one round at once (W5). Every probe
// takes its place in the queue of the core (urltest_rr_queue_lx.go, §4.7).
//
// The check is started only by a soft sign on a pool node and judges the whole
// pool: the first probes of every node; after H with no answer the candidates in
// batches of the pool size, at most k batches (rrCandidateBatches); the first
// ANSWER is the control; a node that has not answered gets its decisive probe when
// its first probe ended with no ANSWER or flew H, in parts of up to U − 1 nodes,
// and a part with a decisive probe with no ANSWER gets one control after. D1
// strikes a node out and empties its slot, D3 writes the answer, D4/D5 end with no
// verdict and no event. The search is started by an empty slot or the slot of a
// quarantined node: candidates only, no pool node, no control; a candidate takes
// the slot by the ANSWER of its own probe (§5). Only one writer changes the slots at
// a time, under the flag of the scheduled run (URLTestGroup.checking), taken for the
// write of a slot only, never for the probes of a round: while a run holds it, the
// run fills the slots itself, and a run or a manual test is never skipped for a round.
//
// A connection test (SPEC 139, lxtrace.IsProbe) gets the retry and records nothing;
// a delay measurement through the group (urltest.IsMeasureLx) gets nothing.

import (
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
	// rrHealYieldWait bounds how long the full run after a network change waits for
	// the aborted check to end.
	rrHealYieldWait = C.TCPTimeout
	// rrCandidateBatches is k: batches of candidates per round, the replacement of a
	// check included (L3).
	rrCandidateBatches = 2
)

// rrHeal is the self-heal state of one round_robin group; it lives in the balancer.
type rrHeal struct {
	running      atomic.Bool                        // a round (a check or a search) is in flight
	checkAccess  sync.Mutex                         // guards ended
	ended        chan struct{}                      // closed when the round in flight ends (yield)
	lastCheckEnd atomic.Pointer[time.Time]          // e, the end of the previous round (W1)
	abort        atomic.Pointer[context.CancelFunc] // cancels the round in flight
	// epochEnd is E, the rrClock of the end of the last freeze (N0): while now < E
	// nothing is inferred or done, and an exchange that started before E takes part
	// in no rule. 0: no event yet (epoch reads it as math.MinInt64).
	epochEnd atomic.Int64
	// cursor is c, the position in the config from which the next round goes on
	// with the candidates; only the holder of running moves it.
	cursor int
	// The pause of rounds (§4.6): step is g as rrHealGap << step; polled is o, the
	// candidates the cursor gave since the last ANSWER of a probe of the group;
	// extraFor is the end of the round whose pause holds the right xr (W5), and
	// extraTaken whether the round of that right started; extra marks the round in
	// flight as the one of xr.
	pauseAccess sync.Mutex
	step        atomic.Int32
	polled      atomic.Int32
	extraFor    *time.Time
	extraTaken  bool
	extra       bool
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
	return rrHealGap << h.step.Load()
}

// pausing reports, at now, whether the pause after the last round still holds.
func (h *rrHeal) pausing(now time.Time) bool {
	end := h.lastCheckEnd.Load()
	return end != nil && now.Sub(*end) < h.pause()
}

// checkReady reports, without a lock, whether a round could start now: none in
// flight, not frozen, the pause over or the right of W5 not spent.
func (h *rrHeal) checkReady() bool {
	return h.readyAt(time.Now())
}

func (h *rrHeal) readyAt(now time.Time) bool {
	if h.running.Load() || h.frozen() {
		return false
	}
	return !h.pausing(now) || h.extraOpen()
}

// extraOpen reports the right xr of the current pause, not spent yet (W5).
func (h *rrHeal) extraOpen() bool {
	h.pauseAccess.Lock()
	defer h.pauseAccess.Unlock()
	return h.extraFor != nil && h.extraFor == h.lastCheckEnd.Load() && !h.extraTaken
}

// beginCheck claims the single round of the group (W1: one at a time, after the
// pause, or the round of the right xr).
func (h *rrHeal) beginCheck() bool {
	return h.beginRoundAt(time.Now())
}

func (h *rrHeal) beginRoundAt(now time.Time) bool {
	if !h.readyAt(now) {
		return false
	}
	h.checkAccess.Lock()
	defer h.checkAccess.Unlock()
	if !h.running.CompareAndSwap(false, true) {
		return false
	}
	h.pauseAccess.Lock()
	h.extra = h.pausing(now)
	if h.extra {
		if h.extraFor == nil || h.extraFor != h.lastCheckEnd.Load() || h.extraTaken {
			h.pauseAccess.Unlock()
			h.running.Store(false)
			return false
		}
		h.extraTaken = true
	}
	h.pauseAccess.Unlock()
	h.ended = make(chan struct{})
	return true
}

// endCheck ends a round and starts its pause (W1) — unless a freeze cut it short
// (L3: the first reason after the freeze gets a round at once). answered: a probe
// of the round had ANSWER; lap: the cursor went round C since the last ANSWER.
// Without an ANSWER the step grows only after a lap (W2); a round of the right xr
// with no ANSWER keeps the step and the end of the pause (W5).
func (h *rrHeal) endCheck(answered, lap bool) {
	h.endRoundAt(time.Now(), answered, lap)
}

func (h *rrHeal) endRoundAt(now time.Time, answered, lap bool) {
	if !h.frozen() {
		h.pauseAccess.Lock()
		if !h.extra || answered {
			if !answered && lap && h.step.Load() < rrPauseSteps {
				h.step.Add(1)
			}
			h.lastCheckEnd.Store(&now)
		}
		h.extra = false
		h.pauseAccess.Unlock()
	}
	h.release()
}

// endIdle ends a round that started no probe (W1): it is no round — no pause, the
// step as it was, and the right xr it took is not spent.
func (h *rrHeal) endIdle() {
	h.pauseAccess.Lock()
	if h.extra {
		h.extraTaken = false
	}
	h.extra = false
	h.pauseAccess.Unlock()
	h.release()
}

// release frees the single round of the group.
func (h *rrHeal) release() {
	h.checkAccess.Lock()
	defer h.checkAccess.Unlock()
	h.running.Store(false)
	if h.ended != nil {
		close(h.ended)
		h.ended = nil
	}
}

// probeAnswered is W3: an ANSWER of any probe of the group brings the step back to
// the base and restarts o.
func (h *rrHeal) probeAnswered() {
	if h.step.Load() != 0 {
		h.step.Store(0)
	}
	if h.polled.Load() != 0 {
		h.polled.Store(0)
	}
}

// grantExtra is W5: an ANSWER of a connection of the group during a pause above
// the base gives one round at once, once per pause. It reports a new right.
func (h *rrHeal) grantExtra() bool {
	return h.grantExtraAt(time.Now())
}

func (h *rrHeal) grantExtraAt(now time.Time) bool {
	if h.step.Load() == 0 || !h.pausing(now) {
		return false
	}
	h.pauseAccess.Lock()
	defer h.pauseAccess.Unlock()
	end := h.lastCheckEnd.Load()
	if end == nil || h.extraFor == end {
		return false
	}
	h.extraFor, h.extraTaken = end, false
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
	h.pauseAccess.Lock()
	h.lastCheckEnd.Store(nil)
	h.step.Store(0)
	h.extraFor, h.extraTaken = nil, false
	h.pauseAccess.Unlock()
	h.lifeAccess.Lock()
	h.lifeStarts, h.udpLifeLast = [rrLifeBurst]int64{}, 0
	h.lifeAccess.Unlock()
	h.abortCheck()
}

func (h *rrHeal) abortCheck() {
	if cancel := h.abort.Load(); cancel != nil {
		(*cancel)()
	}
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

// yield waits, at most rrHealYieldWait, for the round in flight to end.
func (h *rrHeal) yield(ctx context.Context) {
	h.checkAccess.Lock()
	ended := h.ended
	h.checkAccess.Unlock()
	if ended == nil {
		return
	}
	timer := time.NewTimer(rrHealYieldWait)
	defer timer.Stop()
	select {
	case <-ended:
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
		if !lxtrace.IsProbe(ctx) && !b.heal.frozen() && !s.rrBeforeEvent(picked.start) { // N0, I4: the retry only
			if tcp {
				b.rrCountConn(member.Tag(), picked.start)
			}
			sign = s.rrOpenFailed(member, picked, class, tcp)
		}
	}
	if (class == rrRefused || class == rrLocal) && picked.key.valid && b.memory.shifts.marked(picked.key.table) {
		return rrRetried{}, false // K5: a marked key keeps the retry of FAIL_NODE only
	}
	// A nested urltest retries on its own (SPEC 054): two attempts per call at most.
	// Only the outermost round_robin group of the chain of one open retries (§5): an
	// inner one, found by the mark of the context, leaves it to the outer.
	if rrNestedURLTest(member) || picked.inner || ctx.Err() != nil {
		return rrRetried{}, false
	}
	retry := s.rrRetryTarget(slots, failedSlot, failed.Tag(), network)
	if retry == nil {
		return rrRetried{}, false
	}
	s.group.balancedRetrying(ctx, retry, failed.Tag())
	dialer, err := adapter.ResolveChainLeaf(ctx, retry)
	if err != nil {
		return rrRetried{}, false
	}
	retryStart := time.Now()
	retryCtx, dial := s.rrTrackDial(rrMarkDial(ctx), b.trackOf(retry.Tag()), retry, dialer, rrClock(retryStart), !tcp)
	if err = open(retryCtx, dialer); err != nil {
		s.logger.DebugContext(ctx, "round_robin retry via ", retry.Tag(), " failed: ", err)
		if !dial.failed() && ctx.Err() == nil && !lxtrace.IsProbe(ctx) && !s.rrBeforeEvent(retryStart) {
			s.rrRetryFailed(retry, retryStart, err, tcp)
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

// rrOpenFailed applies the rules to a failed open of the pick's own slot (not a
// retry): S1, S2, K1, K2/K4 and the Q1 sign. It returns the sign the first byte of
// the retry completes.
func (s *URLTest) rrOpenFailed(member adapter.Outbound, picked rrPick, class rrClass, tcp bool) *rrSign {
	t := s.balancer.trackOf(member.Tag())
	if t == nil {
		return nil
	}
	a, now := rrClock(picked.start), rrClock(time.Now())
	eligible := tcp && !picked.detour
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
		sign := s.rrFailSign(t, picked, member.Tag(), now, tcp) // Q1, before the probe whose answer settles it
		s.rrLifeProbe(member, t, a, false)                      // S1
		return sign
	default:
		if tcp {
			t.suspectAt(a, now) // S2: the bypass at once
		}
		s.rrSoftSign(picked.key, picked.shift, member.Tag(), a, now, false, eligible)
		var sign *rrSign
		if s.rrLiveOthers(member.Tag(), a, now) {
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

// rrRetryFailed applies S1/S2 to a failed retry through retry: a sign of its node,
// with no key shift and no K2 memory (§4.4: the slot of the retry), and no Q1 count
// (a failed retry has no retry of its own).
func (s *URLTest) rrRetryFailed(retry adapter.Outbound, start time.Time, err error, tcp bool) {
	class := s.rrOutcome(retry, err)
	t := s.balancer.trackOf(retry.Tag())
	if class == rrCancel || t == nil {
		return
	}
	if tcp {
		s.balancer.rrCountConn(retry.Tag(), start)
	}
	a := rrClock(start)
	switch class {
	case rrLocal:
		if !s.rrSiteOnOpen(retry) {
			s.rrLifeProbe(retry, t, a, false) // S1
		}
	case rrFailTimeout:
		if tcp {
			t.suspectAt(a, rrClock(time.Now())) // S2
		}
		s.rrLifeProbe(retry, t, a, !tcp)
	default:
		s.rrLifeProbe(retry, t, a, false) // S1
	}
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

// rrStartCheck starts the check of the pool when the round of the group is free and
// the pause is over (or the right xr of W5 is open); it reports whether it did. Only
// a soft sign on a pool node asks for it (§4.2).
func (s *URLTest) rrStartCheck() bool {
	if !s.balancer.heal.beginCheck() {
		return false
	}
	go s.rrHealCheck()
	return true
}

// rrStartSearch starts the replacement search (§5): an empty slot or the slot of a
// quarantined node with no replacement; no probe of a pool node, no control. With
// no candidate, or while a run holds its flag (the run fills the slots itself),
// there is nothing to probe and no round (W1).
func (s *URLTest) rrStartSearch() bool {
	if s.group.checking.Load() || !s.rrHasCandidate() || !s.balancer.heal.beginCheck() {
		return false
	}
	go s.rrHealSearch()
	return true
}

// rrExtraRound is the round of the right xr (W5): the check when a suspicion still
// asks for it (D4), else the search when a slot is empty; with no reason the right
// waits for one until the end of the pause.
func (s *URLTest) rrExtraRound() {
	now, epoch := rrClock(time.Now()), s.balancer.heal.epoch()
	for _, entry := range s.balancer.slotsSnapshot() {
		if entry.track != nil && entry.track.wantCheck.Load() && entry.track.suspected(now, epoch) {
			s.rrStartCheck()
			return
		}
	}
	if s.rrSlotToFill() {
		s.rrStartSearch()
	}
}

// rrSlotToFill reports a slot to fill (§5): empty (D1), with a quarantined node, or
// with a node without history.
func (s *URLTest) rrSlotToFill() bool {
	for _, entry := range s.balancer.slotsSnapshot() {
		if s.rrSlotEmpty(entry.tag) {
			return true
		}
	}
	return false
}

// rrSlotEmpty reports a slot the pick skips and the search fills (§5): no node, a
// quarantined one, or one without history (a dead occupant the run left, a leaf
// whose shared history D1 of another group erased). It is no slot of the pool for
// the check (I9).
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
	answered   []bool      // a probe of the pool node answered in this check (D3)
	firstAt    []time.Time // the start of the first probe of the pool node
	firstEnded []bool
	voided     []bool // the decisive probe of the pool node was cancelled: no D1 (U3)
	partOf     []int  // the decisive part of the pool node; -1 for none
	waiting    []int  // pool nodes of a part whose decisive probe waits (step 3)
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
	// held by its own part whose probes wait for the loop. requests counts the
	// goroutines that wait for places; a pending grant counts as flying.
	grants   chan func()
	requests sync.WaitGroup
	// reserved are the places of granted decisive parts not used yet: by a decisive
	// probe waiting to start or by a control after.
	reserved int
	launched int // the probes the round started; none: no round (W1)
}

// request asks the queue for n places and hands then to the loop once they are
// granted (U1); with the round over it gets nothing to do.
func (c *rrCheck) request(n int, then func()) {
	c.flying++
	c.requests.Add(1)
	go func() {
		defer c.requests.Done()
		if !c.queue.acquire(c.ctx, &c.s.balancer.heal.queue, n) {
			c.grants <- func() {}
			return
		}
		c.grants <- then
	}()
}

// settle ends a round: the requests still waiting give up (the context is
// cancelled), and the places granted meanwhile go back through probes that end at
// once.
func (c *rrCheck) settle() {
	c.requests.Wait()
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
// end.
func (c *rrCheck) launch(role rrCheckRole, index int, node adapter.Outbound, from time.Time) {
	c.flying++
	c.launched++
	batch := c.batches
	// The decisive probe of a node whose first probe still flies flies beside it from
	// the moment of step 3, not after its end (U3).
	alone := role == rrRoleDecisive && !c.firstEnded[index]
	go func() {
		probe := c.s.group.rrHealProbe(c.ctx, node, from, alone)
		c.queue.release(1)
		c.results <- rrCheckResult{role, index, batch, probe}
	}()
}

// launchAll asks for the probes of nodes in parts of at most U (U1); each part
// starts when its places are granted.
func (c *rrCheck) launchAll(role rrCheckRole, first int, nodes []adapter.Outbound) {
	for start := 0; start < len(nodes); start += rrQueueCap {
		end := min(start+rrQueueCap, len(nodes))
		c.request(end-start, func() {
			for i := start; i < end; i++ {
				if role == rrRoleFirst {
					c.firstAt[first+i] = time.Now()
				}
				c.launch(role, first+i, nodes[i], c.x)
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
		c.firstEnded[r.index] = true
		if r.probe.answered {
			if !c.answered[r.index] {
				c.answered[r.index] = true
				c.s.rrAlive(r.probe) // D3
			}
			c.found(c.pool[r.index])
		}
		if len(c.waiting) > 0 {
			c.startDecisive()
		}
	case rrRoleDecisive:
		part := c.parts[c.partOf[r.index]]
		if r.probe.answered {
			if !c.answered[r.index] {
				c.answered[r.index] = true
				c.s.rrAlive(r.probe) // D3
			}
		} else {
			part.missed = true
		}
		c.partDone(c.partOf[r.index])
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
		c.firstEnded[r.index] = true
		if len(c.waiting) > 0 {
			c.startDecisive()
		}
	case rrRoleDecisive:
		c.voided[r.index] = true
		c.partDone(c.partOf[r.index])
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
		if !c.answered[i] {
			nodes = append(nodes, i)
		}
	}
	for start := 0; start < len(nodes); start += rrQueueCap - 1 {
		end := min(start+rrQueueCap-1, len(nodes))
		part := len(c.parts)
		c.parts = append(c.parts, &rrDecisivePart{open: end - start})
		for _, i := range nodes[start:end] {
			c.partOf[i] = part
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
		case c.answered[i]:
			c.reserved--
			c.queue.release(1)
			c.partDone(c.partOf[i])
		case c.firstEnded[i] || !c.firstAt[i].IsZero() && now.Sub(c.firstAt[i]) >= rrHangAfter:
			c.reserved--
			c.launch(rrRoleDecisive, i, c.pool[i], c.b0)
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
		c.launch(rrRoleAfter, index, c.control, time.Now())
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

// takeReady returns the next candidate that answered and may take a slot.
func (c *rrCheck) takeReady() adapter.Outbound {
	b := c.s.balancer
	for len(c.ready) > 0 {
		node := c.ready[0]
		c.ready = c.ready[1:]
		if !b.inPool(node.Tag()) && !b.isQuarantined(node.Tag()) {
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
	heal.abort.Store(&cancel)
	slots := s.balancer.slotsSnapshot()
	c := &rrCheck{s: s, ctx: ctx, queue: s.group.rrQueue(), x: time.Now(), size: len(slots), seen: make(map[string]bool)}
	defer func() {
		heal.abort.Store(nil)
		cancel()
		c.settle()
		if c.launched == 0 {
			heal.endIdle() // W1: a round that probed nothing is no round
			return
		}
		heal.endCheck(c.anyAnswer, s.rrLap())
	}()
	if heal.aborted(ctx) {
		return
	}
	round(c)
}

// rrHealCheck is the check of the pool (§4.2) followed by D1 and the replacement
// (§5). It owns the round taken by beginCheck.
func (s *URLTest) rrHealCheck() {
	s.rrHealRound(func(c *rrCheck) {
		for _, entry := range s.balancer.slotsSnapshot() {
			if s.rrSlotEmpty(entry.tag) {
				continue // a quarantined node or one without history takes no part; a struck one left its slot (§4.2)
			}
			if node, loaded := s.outbound.Outbound(entry.tag); loaded && common.Contains(node.Network(), N.NetworkTCP) {
				c.pool = append(c.pool, node)
				c.seen[node.Tag()] = true
			}
		}
		n := len(c.pool)
		c.results = make(chan rrCheckResult, 3*n+2*rrCandidateBatches*c.size+1)
		c.grants = make(chan func(), 2*n+2*rrCandidateBatches*c.size+1)
		c.answered, c.firstEnded, c.firstAt = make([]bool, n), make([]bool, n), make([]time.Time, n)
		c.voided = make([]bool, n)
		c.partOf = make([]int, n)
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

// rrLap is lap (§4.6): the cursor gave as many candidates as C holds for it since
// the last ANSWER of a probe of the group, or C holds none.
func (s *URLTest) rrLap() bool {
	g, b := s.group, s.balancer
	walk := 0
	for _, node := range g.outbounds {
		if !s.rrCandidate(node) || s.rrLiveHistory(node) != nil {
			continue // in the pool, out of reach, or polled first by its live history (§5)
		}
		walk++
	}
	return int(b.heal.polled.Load()) >= walk
}

// rrStrike is D1 for every pool node whose decisive probe had no ANSWER while the
// control after its part answered, read at the verdict: no ANSWER via the node
// that started at or after x (proved), no history written after x. The node
// leaves its slot at once, the slot is empty even with no replacement (R1), and a
// second D1 since the scheduled run is the quarantine of Q4.
func (s *URLTest) rrStrike(ctx context.Context, c *rrCheck) {
	b := s.balancer
	for i, node := range c.pool {
		if c.answered[i] || c.voided[i] || c.control == nil || b.heal.aborted(ctx) {
			continue // D3, no outcome of the decisive probe (U3), or no control (D5)
		}
		if part := c.partOf[i]; part >= len(c.parts) || !c.parts[part].afterOK {
			continue // D4
		}
		t := b.trackOf(node.Tag())
		if t == nil || t.proved.Load() >= rrClock(c.x) {
			continue
		}
		if !s.group.history.DeleteURLTestHistoryNotAfterLx(RealTag(node, N.NetworkTCP), c.x) {
			continue // D1: a scheduled run answered for the node since x, no verdict
		}
		others := false
		for _, entry := range b.slotsSnapshot() {
			if entry.tag != node.Tag() && !s.rrSlotEmpty(entry.tag) {
				others = true
				break
			}
		}
		b.rrForget(node.Tag())         // Q3: D1 drops the counters
		b.memory.k2.forget(node.Tag()) // K2: D1 erases the node's records
		s.rrDead(t)
		b.clearSlot(node.Tag())
		s.logger.Info("round_robin: ", node.Tag(), " struck out as dead; control ", c.control.Tag())
		if b.rrStruck(node.Tag()) >= 2 && others {
			b.rrQuarantine(node.Tag()) // Q4: the second D1 since the scheduled run
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
		if s.rrSlotEmpty(entry.tag) {
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
			if dead != "" && !s.rrSlotEmpty(dead) {
				break // its node got history back meanwhile: the slot is no longer to fill
			}
			if !s.rrTakeChecking(ctx) {
				return // a run holds the flag: the run fills the slots itself
			}
			replaced := b.replaceSlot(dead, node.Tag())
			s.group.checking.Store(false)
			if replaced {
				s.logger.Info("round_robin: ", node.Tag(), " takes the slot of ", rrSlotName(dead))
				break
			}
		}
	}
}

func rrSlotName(tag string) string {
	if tag == "" {
		return "a struck node"
	}
	return tag
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
// it gave (I8), and o counts the ones it gave (§4.6).
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
		b.heal.polled.Add(1)
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

// rrTakeChecking claims URLTestGroup.checking for a slot change of the self-heal;
// it fails when the flag is busy (a run does the same work) or the round has been
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

// lx:end SPEC 138
