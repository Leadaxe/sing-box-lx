package group

// lx:begin SPEC 138 round_robin self-heal after a failed dial
//
// Stage 1 of SPEC 138 §3: a TCP connection of a round_robin group that does not
// open (any error but a client cancel; a UDP open the same way, stage 3,
// urltest_rr_udp_lx.go) is retried once through the next live
// slot of the pool whose node is not a nested urltest (that one retries on its
// own), and the group checks the failed node A and its neighbour B — the next
// live slot, nested or not — in the background:
//   - the probes do not write: a direct urltest.URLTest of the leaf (RealTag)
//     with the group link and C.TCPTimeout; history changes only by verdict, so
//     a blink of the local network does not strike every node at once;
//   - the failure is pending on A until A proves alive after it (stage 2,
//     urltest_rr_track_lx.go): a first byte through A, an answer of a probe of A
//     that started after it, history of A written after it. A failed open starts
//     the check at once; a close without a byte leaves the probe to the next pick
//     of A rrVerdictWait later, if no first byte through A has settled it by then.
//     Every probe is live: A, the control — B, the third node, the candidates of
//     a replacement search that serves as the control — history says nothing
//     about whether the network answers now;
//   - A silent, B answered: A is dead — its history is dropped (pick skips the
//     slot at once) and the slot gets a replacement from outside the pool; a
//     candidate of that replacement may count by fresh history, A is already
//     proved dead by a live answer;
//   - A answered: A is alive, the destination is not reachable through it; its
//     fresh answer is written, and its failures pending since before the probe
//     shift their keys (urltest_rr_shift_lx.go);
//   - both silent: a third node answers → both dead; it is silent too → the
//     local network is down, nothing changes and the group freezes;
//   - no neighbour: the replacement search is the control;
//   - the history of a dead node is dropped only if it is not newer than the
//     start of its probe: a scheduled check that found it alive meanwhile wins,
//     and the verdict about that node is dropped.
//
// A node that accepts and stays silent gives no failure at all. A pick of its slot
// that finds an unfinished connection through it older than rrHangAfter — a dial
// in flight, or a connection whose client sent something and got no byte — is a
// reason for a check (urltest_rr_track_lx.go); so is a client that cancelled the
// dial or closed such a connection after that long. No retry, shift or count
// follows from a reason. It is dropped for a node proved alive meanwhile or with
// fresh history, and its checks keep their own gap, rrHangGap (rrHang). The dead
// verdict cancels the node's dials in flight (they retry once through the next
// live slot, with no new check) and closes its connections waiting for a first
// byte.
// An interrupt of the group, its Close and a connection test are no reason.
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
// A delay measurement through the group (urltest.IsMeasureLx: an outer group's
// check, the clash API) gets nothing: no retry, no watch, no check — it measures
// the node as it is.

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
	// rrHangAfter — a client that gives up on a connection with no byte after
	// waiting this long is a reason for a check: a hanging node gives no failure.
	rrHangAfter = 5 * time.Second
	// rrHangGap — no new check from a client that gave up (rrHang) this long after
	// the previous such check ended. A steady stream of silent UDP flows closed by
	// their timeout would otherwise probe the nodes several times a minute.
	rrHangGap = 60 * time.Second
)

// rrHeal is the self-heal state of one round_robin group; it lives in the balancer.
// The time marks are time.Now() values: they keep the monotonic clock reading, so
// a wall clock step does not stretch the gap or the freeze.
type rrHeal struct {
	running      atomic.Bool                        // a check is in flight
	lastCheckEnd atomic.Pointer[time.Time]          // end of the previous check
	lastHangEnd  atomic.Pointer[time.Time]          // end of the previous check from a reason (rrHang)
	frozenUntil  atomic.Pointer[time.Time]          // end of the freeze
	networkRuns  atomic.Int32                       // full runs after a network change in flight
	abort        atomic.Pointer[context.CancelFunc] // cancels the check in flight
	lastByte     atomic.Int64                       // rrClock of the last first byte of any connection (stage 2)
	lastFreeze   atomic.Int64                       // rrClock of the last freeze: it voids the failures pending before it (stage 2)
	interrupting atomic.Int32                       // an interrupt of the group is closing its connections
}

// rrInterrupting marks the interrupt of the group's connections (a selection
// update) until the returned function runs: a connection it closes is no client
// giving up. A no-op outside round_robin.
func (g *URLTestGroup) rrInterrupting() func() {
	if g.balancer == nil {
		return func() {}
	}
	heal := &g.balancer.heal
	heal.interrupting.Add(1)
	return func() {
		heal.interrupting.Add(-1)
	}
}

// beginCheck claims the single check slot of the group, unless the previous check
// ended less than rrHealGap ago.
func (h *rrHeal) beginCheck() bool {
	if end := h.lastCheckEnd.Load(); end != nil && time.Since(*end) < rrHealGap {
		return false
	}
	return h.running.CompareAndSwap(false, true)
}

// beginHangCheck claims the single check slot of the group for a check from a
// reason (rrHang), unless the previous such check ended less than rrHangGap ago.
// It ignores the gap of the failure checks and does not set it: a reason must not
// take the slot from the check of a real failure.
func (h *rrHeal) beginHangCheck() bool {
	if end := h.lastHangEnd.Load(); end != nil && time.Since(*end) < rrHangGap {
		return false
	}
	return h.running.CompareAndSwap(false, true)
}

// hangReady reports, without a lock, whether a check from a reason could start now:
// none in flight, the gap of such checks passed, not frozen.
func (h *rrHeal) hangReady() bool {
	if h.running.Load() || h.frozen() {
		return false
	}
	end := h.lastHangEnd.Load()
	return end == nil || time.Since(*end) >= rrHangGap
}

func (h *rrHeal) endHangCheck() {
	now := time.Now()
	h.lastHangEnd.Store(&now)
	h.running.Store(false)
}

func (h *rrHeal) endCheck() {
	now := time.Now()
	h.lastCheckEnd.Store(&now)
	h.running.Store(false)
}

// freeze freezes the self-heal for rrHealFreeze and aborts the check in flight.
func (h *rrHeal) freeze() {
	now := time.Now()
	until := now.Add(rrHealFreeze)
	h.frozenUntil.Store(&until)
	h.lastFreeze.Store(rrClock(now))
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
		s.balancer.rrResetMarks() // stage 2: a network change lifts the marks and the quarantine
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
	heal.lastFreeze.Store(rrClock(time.Now()))
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

// rrHealDial handles a failed dial of a round_robin group: it records the failure,
// starts the check of the node when due and returns the connection of the retry
// through the next live slot. picked is the pick of the connection: its key, the
// key's shift then, its start and its dial. ok is false when there is no retry or
// it failed too: the caller returns dialErr. A UDP dial (a connected UDP flow, a
// DNS transport) is handled as a UDP open (rrHealListen): retry and check, no key
// shift.
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
	// Stage 2: the first byte of the retry confirms the failure of the first node.
	picked.dial = retry.dial
	return s.rrWatch(ctx, network, retry.dialer, conn, picked, retry.confirmFor, retry.failure), true
}

// rrRetried is an opened retry: the chain leaf it went through, the pool member
// whose failure it confirms ("" when none waits for it), that failure and the
// tracked dial of the retry.
type rrRetried struct {
	dialer     adapter.Outbound
	confirmFor string
	failure    *rrFailure
	dial       *rrDial
}

// rrHealRetry is the part of a failed open shared by TCP and UDP (rrHealDial,
// rrHealListen): it records the failure of the node as pending on it, starts the
// check when due and opens the retry through the next live slot that takes
// network, with open on the chain leaf of the retry node. ok is false when there
// is no retry or it failed too. A UDP failure shifts no key (§3.8): its key is
// dropped once the address-problem mark is checked. struck is a dial the dead
// verdict on its node cancelled: the verdict is in, so it records nothing and
// starts no check, only the retry.
func (s *URLTest) rrHealRetry(ctx context.Context, network string, failed adapter.Outbound, dialErr error, picked rrPick, struck bool, open func(ctx context.Context, dialer adapter.Outbound) error) (rrRetried, bool) {
	if urltest.IsMeasureLx(ctx) {
		return rrRetried{}, false // a delay measurement shows the node as it is: no retry, no record
	}
	if errors.Is(ctx.Err(), context.Canceled) || !struck && errors.Is(dialErr, context.Canceled) {
		// A client cancel is no failure; after a long wait it is a reason for a check.
		if !lxtrace.IsProbe(ctx) {
			s.rrHang(failed.Tag(), picked.start)
		}
		return rrRetried{}, false
	}
	if s.balancer.heal.frozen() {
		return rrRetried{}, false
	}
	// A UDP open that failed puts the node under suspicion, not the address: an
	// address-problem mark learned by TCP does not stop its retry and check, and a
	// UDP failure shifts no key and counts nothing in the slot counters (§3.8).
	tcp := N.NetworkName(network) == N.NetworkTCP
	if !tcp {
		picked.key = rrKey{}
	}
	probe := lxtrace.IsProbe(ctx)
	if !probe && !struck {
		// Stage 2: an address-problem key gets no retry, no check and no shift.
		if picked.key.valid && s.balancer.memory.shifts.marked(picked.key.table) {
			return rrRetried{}, false
		}
	}
	slots := s.balancer.slotsSnapshot()
	failedSlot := slotIndex(slots, failed.Tag())
	// failed may be the chain hop of the node; the check needs the pool member.
	member, loaded := s.outbound.Outbound(failed.Tag())
	if !loaded || failedSlot < 0 && !struck {
		return rrRetried{}, false // a fallback pick outside the pool (cold start)
	}
	// Stage 2: the failure is pending on the node until it proves alive or dead; a
	// connection test leaves the counters and the shifts alone. A failed open starts
	// the check at once, as stage 1 does; when it cannot start (one in flight, the
	// gap), the next pick of the node asks again unless a first byte through the
	// node settles the failure first.
	var failure *rrFailure
	if !probe && !struck {
		if tcp {
			s.balancer.rrCountConn(member.Tag(), &s.balancer.heal)
		}
		failure = s.rrNewFailure(picked, member.Tag())
		s.rrPend(member, failure, false, true)
	}
	// A nested urltest retries on its own (SPEC 054 or this one), so it is neither
	// retried nor taken for the retry: two attempts per call at most. A context
	// that is already done has no time for a second attempt.
	if rrNestedURLTest(member) || ctx.Err() != nil {
		return rrRetried{}, false
	}
	usable := func(tag string) bool {
		if tag == failed.Tag() || !s.slotAlive(tag) || !s.slotSupports(tag, network) {
			return false
		}
		node, loaded := s.outbound.Outbound(tag)
		return loaded && !rrNestedURLTest(node)
	}
	var retry adapter.Outbound
	if failedSlot < 0 {
		// Struck and replaced meanwhile: the first usable slot.
		for _, entry := range slots {
			if usable(entry.tag) {
				retry, _ = s.outbound.Outbound(entry.tag)
				break
			}
		}
	} else if i, found := s.balancer.nextLiveSlot(slots, failedSlot, usable); found {
		retry, _ = s.outbound.Outbound(slots[i].tag)
	}
	if retry == nil {
		return rrRetried{}, false
	}
	s.group.balancedRetrying(ctx, retry, failed.Tag())
	dialer, err := adapter.ResolveChainLeaf(ctx, retry)
	if err != nil {
		return rrRetried{}, false
	}
	retryCtx, dial := s.rrTrackDial(ctx, s.balancer.trackOf(retry.Tag()), retry, dialer)
	if err = open(retryCtx, dialer); err != nil {
		retryStruck := dial.failed()
		s.logger.ErrorContext(ctx, "round_robin retry via ", retry.Tag(), ": ", err)
		// A client that gave up meanwhile is no failure of the retry's node, nor is
		// a retry cancelled by the dead verdict on its node.
		if failure != nil && !retryStruck && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			if tcp {
				s.balancer.rrCountConn(retry.Tag(), &s.balancer.heal)
			}
			failure.retryFailed(retry.Tag())
			s.rrPend(retry, failure, true, false)
		}
		return rrRetried{}, false
	}
	dial.opened()
	// Only an opened retry moves the connection's node and Now(): a failed one
	// leaves the first node on record.
	s.group.balancedRetried(ctx, retry)
	if struck {
		s.logger.DebugContext(ctx, "round_robin: ", failed.Tag(), " struck out as dead during the dial (", network, "), retried via ", retry.Tag())
	} else {
		s.logger.DebugContext(ctx, "round_robin: ", failed.Tag(), " failed (", network, "), retried via ", retry.Tag())
	}
	retried := rrRetried{dialer: dialer, failure: failure, dial: dial}
	if failure != nil {
		retried.confirmFor = member.Tag()
	}
	return retried, true
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

// rrStore writes the answer of a probe to history; a fresh or silent one is left as is.
func (g *URLTestGroup) rrStore(probe rrProbe) {
	if probe.answered && !probe.fresh {
		g.history.StoreURLTestHistory(probe.leaf, &adapter.URLTestHistory{Time: time.Now(), Delay: probe.delay})
	}
}

// rrHealCheck probes the node A and its neighbour B and acts on the verdict (SPEC
// 138 §3.3). It owns the check slot taken by beginCheck. It runs on a context that
// the group's Close and a freeze cancel, and stops at once then. A node that
// answered settles its failures pending since before its probe (stage 2); a node
// found dead drops them and loses its unfinished connections.
func (s *URLTest) rrHealCheck(member adapter.Outbound, neighbour adapter.Outbound, hang bool) {
	g := s.group
	heal := &s.balancer.heal
	// The gap bounds probes: a check that was aborted before its probes leaves no
	// gap. A check from a reason (hang) sets its own gap only.
	var probed bool
	defer func() {
		if hang {
			heal.endHangCheck()
		} else if probed {
			heal.endCheck()
		} else {
			heal.running.Store(false)
		}
	}()
	ctx, cancel := context.WithCancel(g.ctx)
	heal.abort.Store(&cancel)
	defer func() {
		heal.abort.Store(nil)
		cancel()
	}()
	if heal.aborted(ctx) {
		return
	}
	probed = true
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
			s.rrSettleProbe(b)
		}
		// A is alive: the destination is not reachable through it; its failures
		// pending since before the probe shift their keys.
		s.rrSettleProbe(a)
		return
	}
	if neighbour == nil {
		s.rrReplaceAsControl(ctx, a)
		return
	}
	if b.answered {
		g.rrStore(b)
		s.rrSettleProbe(b)
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
			s.rrSettleProbe(c)
			s.rrStrike(ctx, []rrProbe{a, b}, c)
			return
		}
		s.rrOwnNetwork(a, b, c)
		return
	}
	s.rrOwnNetwork(a, b)
}

// rrHang is a client that gave up on a connection through member with no byte:
// it cancelled the dial, or closed the connection after it sent something (a UDP
// flow closed with packets sent and none received). After rrHangAfter or more
// since the connection started it is a reason for a check: a node that accepts and
// stays silent gives no failure at all. It is only a reason — no retry, no shift,
// no count: the check strikes a dead node out, freezes on the own network, and
// writes the answer of a live one. Silent flows are common (QUIC to a blocked
// site, NTP, STUN), so the reason is dropped when the node proved alive since the
// connection started — another connection through it got its first byte, or its
// history was written — or when its history is younger than rrHealFresh; and the
// checks from reasons keep their own gap, rrHangGap, without the gap of the
// failure checks (one check in flight is shared). An interrupt of the group, its
// Close and a connection test are no reason; the freeze holds as for failures.
func (s *URLTest) rrHang(member string, start time.Time) {
	b := s.balancer
	if start.IsZero() || time.Since(start) < rrHangAfter || s.group.ctx.Err() != nil || b.heal.frozen() || b.heal.interrupting.Load() > 0 {
		return
	}
	node, loaded := s.outbound.Outbound(member)
	if !loaded {
		return
	}
	if b.rrByteSince(member, rrClock(start)) {
		return
	}
	if history := s.group.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)); history != nil && (history.Time.After(start) || time.Since(history.Time) < rrHealFresh) {
		return
	}
	if s.rrStartCheck(node, true) {
		s.logger.Debug("round_robin: the client gave up on a connection through ", member, " with no byte after ", time.Since(start).Round(time.Second), "; checking the node")
	}
}

// rrStartCheck starts the check of the pool member node and its neighbour when the
// check slot is free; it reports whether it started one. hang is a check from a
// reason (rrHang): it keeps its own gap.
func (s *URLTest) rrStartCheck(node adapter.Outbound, hang bool) bool {
	b := s.balancer
	slots := b.slotsSnapshot()
	index := slotIndex(slots, node.Tag())
	if index < 0 {
		return false
	}
	var neighbour adapter.Outbound
	if i, found := b.nextLiveSlot(slots, index, func(tag string) bool {
		return s.slotAlive(tag) && s.slotSupports(tag, N.NetworkTCP)
	}); found {
		neighbour, _ = s.outbound.Outbound(slots[i].tag)
	}
	if hang {
		if !b.heal.beginHangCheck() {
			return false
		}
	} else if !b.heal.beginCheck() {
		return false
	}
	go s.rrHealCheck(node, neighbour, hang)
	return true
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
// changes — no connection is cancelled or closed — and the self-heal freezes,
// which voids every pending failure.
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
// skips its slot from now on), its pending failures are dropped, its unfinished
// connections end (rrDead) and its slot gets a replacement.
func (s *URLTest) rrStrike(ctx context.Context, dead []rrProbe, control rrProbe) {
	struck := make([]rrProbe, 0, len(dead))
	for _, probe := range dead {
		if !s.rrDropHistory(probe) {
			continue
		}
		struck = append(struck, probe)
		s.balancer.rrForget(probe.member) // stage 2: its failures while dead do not follow it
		s.logger.Info("round_robin: ", probe.member, " struck out as dead: ", rrProbeSummary(probe), "; control ", rrProbeSummary(control))
		if !s.balancer.heal.aborted(ctx) {
			s.rrDead(s.balancer.trackOf(probe.member))
		}
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
	track := s.balancer.trackOf(a.member) // before the replacement takes the slot
	candidate, probed, found := s.rrReplace(ctx, a, true)
	if s.balancer.heal.aborted(ctx) {
		return
	}
	if found {
		s.balancer.rrForget(a.member) // stage 2: its failures while dead do not follow it
		s.logger.Info("round_robin: ", a.member, " struck out as dead: ", rrProbeSummary(a), "; control ", rrProbeSummary(candidate))
		s.rrDead(track)
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
	return s.rrReplaceNode(ctx, dead, asControl, false)
}

// rrReplaceNode is rrReplace; quarantined refills the slot of a quarantined node,
// which keeps its history (stage 2).
func (s *URLTest) rrReplaceNode(ctx context.Context, dead rrProbe, asControl bool, quarantined bool) (rrProbe, int, bool) {
	g := s.group
	if !asControl && !quarantined && g.history.LoadURLTestHistory(dead.leaf) != nil {
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
// ones with history first, fastest first. A quarantined node is no candidate.
func (g *URLTestGroup) rrCandidates(byDelay bool) []adapter.Outbound {
	inPool := make(map[string]bool)
	for _, tag := range g.balancer.poolTags() {
		inPool[tag] = true
	}
	var withHistory []candidate
	candidates := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if inPool[detour.Tag()] || !common.Contains(detour.Network(), N.NetworkTCP) || g.balancer.isQuarantined(detour.Tag()) {
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
