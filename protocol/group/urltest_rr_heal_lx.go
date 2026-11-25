package group

// lx:begin SPEC 138 round_robin self-heal: outcomes, the retry, the check and the replacement
//
// The rules named here are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/
// ALGORITHM.md.
//
// A failed open of a round_robin connection is classified (§2): CANCEL (the client
// or the group gave up: nothing follows), LOCAL, an explicit REFUSE_DST (a code of
// the node's own leaf; a refused or reset open of a leaf that goes to the site
// itself), FAIL_NODE by an active refusal or by a timeout. Outside a freeze (N0) it
// is retried once through another slot of the pool whose leaf is no nested urltest,
// the next one with no suspicion first, else the next one under suspicion (§5); a
// key marked by K5 keeps only the retry of FAIL_NODE. The sign then
// goes to its rules: S1 (a fast refusal: the life probe, the bypass only after it
// failed), S2 (a timeout: the bypass and the life probe at once), K1 (the key
// shifts at once), K2/K4 (the memory of a soft sign) and Q1 (the count).
//
// The check (§4.2) runs one at a time per group, not earlier than rrHealGap after
// the previous one ended, and judges the whole pool: the first probes of every
// node; after H with no answer the candidates in batches of the pool size, at most
// k batches (rrCandidateBatches); the first ANSWER is the control, and every node
// that has not answered gets its decisive probe then; a decisive probe with no
// ANSWER gets a control after it. D1 strikes a node out, D3 writes the answer,
// D4/D5 end with no verdict and no event. A node struck out and left in its slot
// is no pool node: it is not judged again, only probed as a candidate. Slots with
// no live node then get a replacement: a candidate that answered its probe in this
// check, else the batches the check left of its k (§5). Only the check writes the slots of the self-heal, under the
// flag of the scheduled run (URLTestGroup.checking).
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
	// rrHealGap — no new check this long after the previous one ended (T).
	rrHealGap = 15 * time.Second
	// rrHealFreeze — the freeze of an event, N3 (T).
	rrHealFreeze = 15 * time.Second
	// rrHealYieldWait bounds how long the full run after a network change waits for
	// the aborted check to end.
	rrHealYieldWait = C.TCPTimeout
	// rrCandidateBatches is k: batches of candidates per check, its replacement
	// included (L3).
	rrCandidateBatches = 2
)

// rrHeal is the self-heal state of one round_robin group; it lives in the balancer.
type rrHeal struct {
	running      atomic.Bool                        // a check is in flight
	checkAccess  sync.Mutex                         // guards ended
	ended        chan struct{}                      // closed when the check in flight ends (yield)
	lastCheckEnd atomic.Pointer[time.Time]          // end of the previous check
	abort        atomic.Pointer[context.CancelFunc] // cancels the check in flight
	// epochEnd is E, the rrClock of the end of the last freeze (N0): while now < E
	// nothing is inferred or done, and an exchange that started before E takes part
	// in no rule. 0: no event yet (epoch reads it as math.MinInt64).
	epochEnd atomic.Int64
	// cursor is c, the position in the config from which the next check goes on
	// with the candidates; only the holder of running moves it.
	cursor int
	// lifeAccess guards the budget of the life probes of the group.
	lifeAccess  sync.Mutex
	lifeStarts  [rrLifeBurst]int64
	lifeNext    int
	udpLifeLast int64
}

// checkReady reports, without a lock, whether a check could start now: none in
// flight, not frozen, the gap over.
func (h *rrHeal) checkReady() bool {
	if h.running.Load() || h.frozen() {
		return false
	}
	end := h.lastCheckEnd.Load()
	return end == nil || time.Since(*end) >= rrHealGap
}

// beginCheck claims the single check slot of the group (L3: one at a time).
func (h *rrHeal) beginCheck() bool {
	if !h.checkReady() {
		return false
	}
	h.checkAccess.Lock()
	defer h.checkAccess.Unlock()
	if !h.running.CompareAndSwap(false, true) {
		return false
	}
	h.ended = make(chan struct{})
	return true
}

// endCheck ends a check and starts its gap — unless a freeze cut it short (L3: the
// first reason after the freeze gets a check at once).
func (h *rrHeal) endCheck() {
	if !h.frozen() {
		now := time.Now()
		h.lastCheckEnd.Store(&now)
	}
	h.checkAccess.Lock()
	defer h.checkAccess.Unlock()
	h.running.Store(false)
	if h.ended != nil {
		close(h.ended)
		h.ended = nil
	}
}

// epoch returns E, the end of the last freeze (rrClock; math.MinInt64 for none).
func (h *rrHeal) epoch() int64 {
	if end := h.epochEnd.Load(); end != 0 {
		return end
	}
	return math.MinInt64
}

// freeze is the event of N3: E := now + T, a signal during a freeze extends it
// from its own moment. The check in flight is aborted and the gaps armed before
// the event are dropped (L3).
func (h *rrHeal) freeze() {
	end := rrClock(time.Now().Add(rrHealFreeze))
	for {
		old := h.epochEnd.Load()
		if old >= end || h.epochEnd.CompareAndSwap(old, end) {
			break
		}
	}
	h.lastCheckEnd.Store(nil)
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

// aborted reports whether the check running on ctx has to stop: the group closed
// or an event came (N0: the check ends with no verdict).
func (h *rrHeal) aborted(ctx context.Context) bool {
	return ctx.Err() != nil || h.frozen()
}

// yield waits, at most rrHealYieldWait, for the check in flight to end.
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
	if b.heal.frozen() {
		return rrRetried{}, false // N0: no retries
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
		if !lxtrace.IsProbe(ctx) && !s.rrBeforeEvent(picked.start) {
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
	if rrNestedURLTest(member) || ctx.Err() != nil {
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
	retryCtx, dial := s.rrTrackDial(ctx, b.trackOf(retry.Tag()), retry, dialer, rrClock(retryStart), !tcp)
	if err = open(retryCtx, dialer); err != nil {
		s.logger.ErrorContext(ctx, "round_robin retry via ", retry.Tag(), ": ", err)
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

// --- the check (§4.2) --------------------------------------------------------------

// rrStartCheck starts the check of the pool when its slot is free and the gap is
// over; it reports whether it did.
func (s *URLTest) rrStartCheck() bool {
	if !s.balancer.heal.beginCheck() {
		return false
	}
	go s.rrHealCheck()
	return true
}

type rrCheckRole uint8

const (
	rrRoleFirst     rrCheckRole = iota // the first probe of a pool node (step 1)
	rrRoleCandidate                    // a candidate of a batch (step 2, §5)
	rrRoleDecisive                     // the decisive probe of a pool node (step 3)
	rrRoleAfter                        // the control after (step 4)
)

type rrCheckResult struct {
	role  rrCheckRole
	index int // the pool node; the candidate for rrRoleCandidate
	batch int
	probe rrProbe
}

// rrCheck is one check in flight; only its own goroutine touches it.
type rrCheck struct {
	s          *URLTest
	ctx        context.Context
	x          time.Time // the start of the check
	pool       []adapter.Outbound
	size       int // |P|: the size of a batch of candidates
	results    chan rrCheckResult
	flying     int
	answered   []bool // a probe of the pool node answered in this check (D3)
	afterOK    []bool // the control after its decisive probe answered (D1)
	control    adapter.Outbound
	candidates []adapter.Outbound // the candidates probed, by index
	ready      []adapter.Outbound // the candidates that answered, in order
	seen       map[string]bool
	batches    int // the batches of candidates of the current attempt
	left       int // the probes of the latest batch in flight
	refill     bool
}

func (c *rrCheck) launch(role rrCheckRole, index int, node adapter.Outbound) {
	c.flying++
	batch := c.batches
	go func() {
		c.results <- rrCheckResult{role, index, batch, c.s.group.rrProbeNode(c.ctx, node)}
	}()
}

// polling reports whether the candidates are still needed: no control yet, or no
// candidate ready for the replacement.
func (c *rrCheck) polling() bool {
	if c.refill {
		return len(c.ready) == 0
	}
	return c.control == nil
}

// nextBatch starts the next batch of candidates, at most k per attempt.
func (c *rrCheck) nextBatch() bool {
	if c.batches == rrCandidateBatches {
		return false
	}
	batch := c.s.rrCandidateBatch(c.seen, c.size)
	if len(batch) == 0 {
		return false
	}
	c.batches++
	c.left = len(batch)
	for _, node := range batch {
		c.candidates = append(c.candidates, node)
		c.launch(rrRoleCandidate, len(c.candidates)-1, node)
	}
	return true
}

// loop takes the outcomes until nothing is in flight or done holds. A new batch of
// candidates starts H after the previous one, or at once when the previous one
// ended with no answer (step 2).
func (c *rrCheck) loop(done func() bool) {
	timer := time.NewTimer(rrHangAfter)
	defer timer.Stop()
	for c.flying > 0 && !done() {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
			if c.polling() {
				c.nextBatch()
			}
			timer.Reset(rrHangAfter)
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
	switch r.role {
	case rrRoleFirst, rrRoleDecisive:
		if r.probe.answered {
			if !c.answered[r.index] {
				c.answered[r.index] = true
				c.s.rrAlive(r.probe) // D3
			}
			c.found(c.pool[r.index])
		} else if r.role == rrRoleDecisive {
			c.launch(rrRoleAfter, r.index, c.control) // step 4: the control after, started at y
		}
	case rrRoleAfter:
		if r.probe.answered {
			c.afterOK[r.index] = true
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

// found is an ANSWER in the check: the first one is the control (b0), and every
// pool node that has not answered gets its decisive probe now (step 3, V7).
func (c *rrCheck) found(node adapter.Outbound) {
	if c.control != nil || c.refill {
		return
	}
	c.control = node
	for i, member := range c.pool {
		if !c.answered[i] {
			c.launch(rrRoleDecisive, i, member)
		}
	}
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

// rrHealCheck is the check of the pool (§4.2) followed by D1 and the replacement
// (§5). It owns the check slot taken by beginCheck and runs on a context the
// group's Close and an event (N0) cancel.
func (s *URLTest) rrHealCheck() {
	heal := &s.balancer.heal
	ctx, cancel := context.WithCancel(s.group.ctx)
	heal.abort.Store(&cancel)
	defer func() {
		heal.abort.Store(nil)
		cancel()
		heal.endCheck()
	}()
	if heal.aborted(ctx) {
		return
	}
	slots := s.balancer.slotsSnapshot()
	c := &rrCheck{s: s, ctx: ctx, x: time.Now(), size: len(slots), seen: make(map[string]bool)}
	for _, entry := range slots {
		if entry.tag == "" || !s.slotAlive(entry.tag) {
			continue // V11: a quarantined node takes no part; a struck one is a candidate only (§4.2)
		}
		if node, loaded := s.outbound.Outbound(entry.tag); loaded && common.Contains(node.Network(), N.NetworkTCP) {
			c.pool = append(c.pool, node)
			c.seen[node.Tag()] = true
		}
	}
	c.results = make(chan rrCheckResult, 3*len(c.pool)+2*rrCandidateBatches*c.size+1)
	c.answered, c.afterOK = make([]bool, len(c.pool)), make([]bool, len(c.pool))
	for i, node := range c.pool {
		c.launch(rrRoleFirst, i, node) // step 1
	}
	c.left = len(c.pool)
	if c.left == 0 {
		c.nextBatch()
	}
	c.loop(func() bool { return false })
	if heal.aborted(ctx) {
		return // N0: no verdict
	}
	if c.control == nil {
		s.logger.Debug("round_robin: no probe answered the check; no verdict") // D5
	}
	s.rrStrike(ctx, c)
	s.rrRefill(ctx, c)
}

// rrStrike is D1 for every pool node whose decisive probe had no ANSWER while the
// control after it answered, read at the verdict: no ANSWER via the node that
// started at or after x (proved), no history written after x.
func (s *URLTest) rrStrike(ctx context.Context, c *rrCheck) {
	b := s.balancer
	for i, node := range c.pool {
		if c.answered[i] || !c.afterOK[i] || b.heal.aborted(ctx) {
			continue // D3, D4, or no control (D5)
		}
		t := b.trackOf(node.Tag())
		if t == nil || t.proved.Load() >= rrClock(c.x) {
			continue
		}
		if !s.group.history.DeleteURLTestHistoryNotAfterLx(RealTag(node, N.NetworkTCP), c.x) {
			continue // D1: a scheduled run answered for the node since x, no verdict
		}
		b.rrForget(node.Tag())         // Q3: D1 drops the counters
		b.memory.k2.forget(node.Tag()) // K2: D1 erases the node's records
		s.rrDead(t)
		s.logger.Info("round_robin: ", node.Tag(), " struck out as dead; control ", c.control.Tag())
	}
}

// rrRefill puts into every slot with no live node (struck, quarantined, empty) a
// candidate that answered its probe in this check, else one of up to k more
// batches (§5); with no answer the slot stays empty until the next check.
func (s *URLTest) rrRefill(ctx context.Context, c *rrCheck) {
	b := s.balancer
	var empty []string
	for _, entry := range b.slotsSnapshot() {
		if entry.tag == "" || !s.slotAlive(entry.tag) {
			empty = append(empty, entry.tag)
		}
	}
	if len(empty) == 0 || !s.rrTakeChecking(ctx) {
		return // nothing to replace, or the scheduled run does the same work
	}
	defer s.group.checking.Store(false)
	c.refill, c.left = true, 0 // L3: the batches of the check count against its k
	for _, dead := range empty {
		for {
			if b.heal.aborted(ctx) {
				return
			}
			if dead != "" && s.slotAlive(dead) {
				break // I5: the struck node answered as a candidate and holds its slot alive again
			}
			node := c.takeReady()
			if node == nil {
				if c.flying == 0 && !c.nextBatch() {
					return // the attempt is over
				}
				c.loop(func() bool { return len(c.ready) > 0 })
				continue
			}
			if b.replaceSlot(dead, node.Tag()) {
				s.logger.Info("round_robin: ", node.Tag(), " replaces ", dead, " in its slot")
				break
			}
		}
	}
}

// rrCandidateBatch returns the next batch of at most size candidates not yet probed
// in this check (§5): those with live history first, fastest first, then the rest
// from the cursor going round in config order; the cursor moves past the last one
// it gave (I8). A node struck out and left in its slot is a candidate as any other.
func (s *URLTest) rrCandidateBatch(seen map[string]bool, size int) []adapter.Outbound {
	g, b := s.group, s.balancer
	usable := func(node adapter.Outbound) bool {
		tag := node.Tag()
		return !seen[tag] && !b.isQuarantined(tag) && (!b.inPool(tag) || !s.slotAlive(tag)) && common.Contains(node.Network(), N.NetworkTCP)
	}
	var alive []candidate
	byTag := make(map[string]adapter.Outbound)
	for _, node := range g.outbounds {
		if !usable(node) {
			continue
		}
		if history := g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)); history != nil {
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
	for step := 0; step < n && len(batch) < size; step++ {
		i := (b.heal.cursor + step) % n
		node := g.outbounds[i]
		if !usable(node) || byTag[node.Tag()] != nil {
			continue
		}
		batch = append(batch, node)
		seen[node.Tag()] = true
		b.heal.cursor = (i + 1) % n
	}
	return batch
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

// lx:end SPEC 138
