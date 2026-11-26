package group

// lx:begin urltest hooks: the lx side of the injection points of the upstream urltest.go
//
// The upstream file keeps the lx state as one embedded field per struct and a
// one-line call (or a three-line "if handled") at each injection point; the
// logic behind those calls lives here and in the files of the specs:
// SPEC 019/138 round_robin (urltest_pool_lx.go, urltest_balance_lx.go, urltest_rr_*_lx.go),
// SPEC 020 probe gating, 050 cancellation, SPEC 054 penalties
// (urltest_penalty_lx.go), SPEC 073 chain, SPEC 116 failover (urltest_failover_lx.go),
// SPEC 125 tick (urltest_tick_lx.go), SPEC 139 guarded selection (urltest_selected_lx.go).

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// lxURLTest is the lx state of URLTest, embedded by value: a zero URLTest (tests)
// is a least_test group with no nil checks needed.
type lxURLTest struct {
	balancer *balancer // SPEC 019 — nil for least_test (default)
	failover bool      // SPEC 116 — mode: failover
}

// lxURLTestGroup is the lx state of URLTestGroup, embedded by value for the same reason.
type lxURLTestGroup struct {
	cancel         context.CancelFunc        // 050 — Close cancels an in-flight run
	lastSelected   common.TypedValue[string] // SPEC 019 — Now() in balanced modes
	balancer       *balancer                 // SPEC 019 — round_robin pool; nil for least_test
	groupTag       string                    // SPEC 020 — set by URLTest.Start (probe gating)
	failover       bool                      // SPEC 116 — set by URLTest.Start
	lastTickRunEnd atomic.Int64              // SPEC 125 — end of the last non-forced probe run, a monotonic rrClock reading
	failoverSlow   atomic.Pointer[tagSet]    // SPEC 126 — confirmed-slow held tags during a failover reselect
	reachability   adapter.ReachabilityReporter
	// SPEC 054 — penalty failover (least_test): tag → счётчик отказов «путь
	// мёртв»; сброс только доказательством жизни (успешный дайл / ответ на пробу).
	// forcedRetestRunning + lastForcedRetest — уровень-триггер аварийного
	// force-прогона с дельта-лимитом от КОНЦА прошлого прогона.
	penalties           sync.Map
	forcedRetestRunning atomic.Bool
	lastForcedRetest    common.TypedValue[time.Time]
}

// --- URLTest ---------------------------------------------------------------------

// initLx reads the lx modes of the options (NewURLTest): SPEC 019 round_robin
// balancer (nil keeps least_test) and SPEC 116 failover, which holds the working
// node until it fails.
func (s *URLTest) initLx(logger log.ContextLogger, options option.URLTestOutboundOptions) error {
	balancer, err := newBalancer(options)
	if err != nil {
		return err
	}
	if warnLegacyTolerance(balancer, options) {
		logger.Warn("urltest: tolerance is ignored in round_robin mode; use balancer.pool_tolerance")
	}
	if balancer == nil && options.Balancer != nil {
		return E.New("urltest: balancer is only valid with mode: round_robin")
	}
	failover := isFailoverMode(options)
	if warnFailoverTolerance(failover, options) {
		logger.Warn("urltest: tolerance is ignored in failover mode")
	}
	s.balancer, s.failover = balancer, failover
	return nil
}

// startLx wires the lx state of a group being started (URLTest.Start, the only
// caller of NewURLTestGroup).
func (s *URLTest) startLx(group *URLTestGroup) {
	// 050 — own the group's context so Close can cancel a run that is already in
	// flight. Without it a test blocked on a half-alive node kept its goroutine
	// and the whole outbound slice alive across box shutdown, piling up one
	// generation of zombies per restart.
	group.ctx, group.cancel = context.WithCancel(group.ctx)
	group.reachability = service.FromContext[adapter.ReachabilityReporter](group.ctx) // SPEC 020 — probe gating
	group.balancer = s.balancer                                                       // SPEC 019 — the health-check drives the pool through it
	group.groupTag = s.Tag()                                                          // SPEC 020 — probe gating needs the group's own tag
	group.failover = s.failover                                                       // SPEC 116
	s.rrStart(group)                                                                  // SPEC 019, SPEC 138 — the pool and its self-heal
}

// selectedNowLx is the node Selected reports before the upstream Select fallback:
// in round_robin the last pick (SPEC 019 — no single selected node), otherwise
// the cached selection read under its guard (SPEC 139).
func (s *URLTest) selectedNowLx(network string) adapter.Outbound {
	if s.balancer != nil {
		if selected, loaded := s.outbound.Outbound(s.group.lastSelected.Load()); loaded {
			return selected
		}
	}
	selectedTCP, selectedUDP := s.group.selectedLx()
	if network == N.NetworkUDP {
		return selectedUDP
	}
	return selectedTCP
}

// pickLx is the pick of a dial or a listen: round_robin picks per connection and
// hands the dial its key (SPEC 019, SPEC 138); least_test and failover take the
// penalty-aware pick (SPEC 054: the emergency mode bypasses the cached selection,
// the normal one is upstream's cache, else Select), traced as the group stage
// (SPEC 139). network is passed as the caller got it.
func (s *URLTest) pickLx(ctx context.Context, network string, destination M.Socksaddr) (adapter.Outbound, rrPick) {
	if s.balancer != nil {
		return s.selectBalanced(ctx, network, destination)
	}
	return s.group.pickForDial(ctx, N.NetworkName(network)), rrPick{}
}

// dialStartLx resolves the pick through the chain hook (SPEC 165: the
// carrier record and a picked direct under a chain hop) and
// returns the context of the open: a round_robin open in flight is tracked and the
// dead verdict cancels it (SPEC 138). Only the open gets that context.
func (s *URLTest) dialStartLx(ctx context.Context, outbound adapter.Outbound, picked *rrPick) (adapter.Outbound, context.Context, error) {
	leafCtx, leaf, err := adapter.ResolveChainLeaf(ctx, outbound)
	if err != nil {
		return nil, nil, err
	}
	return leaf, s.rrDialStart(leafCtx, leaf, picked), nil
}

// dialedLx takes a dial that opened: in least_test it is the proof of life of the
// node (SPEC 054, a probe excepted — SPEC 139); round_robin watches for the first
// byte (SPEC 138).
func (s *URLTest) dialedLx(ctx context.Context, network string, outbound adapter.Outbound, conn net.Conn, picked rrPick) net.Conn {
	if s.balancer == nil {
		s.group.penaltyResetOnDial(ctx, RealTag(outbound, N.NetworkTCP))
	}
	return s.rrWatchConn(ctx, network, outbound, conn, picked)
}

// dialFailedLx takes a dial that failed, after the history step
// (deleteHistoryOnDialError); ok returns the connection of a second attempt.
//
// least_test (SPEC 054): «путь мёртв» → штраф + один fallback-дайл; успех переносит
// выбор группы на fallback (без Interrupt).
//
// round_robin (SPEC 019 v2 / SPEC 138): a dial error by itself does not touch the
// pool or the history: the cause is unknown (dead node vs. dead destination vs.
// local network drop). It is retried once through the next live slot and starts a
// background check of the node; only the check's verdict and the health-check
// change history and pool membership.
func (s *URLTest) dialFailedLx(ctx context.Context, network string, destination M.Socksaddr, outbound adapter.Outbound, err error, picked rrPick) (net.Conn, bool) {
	if s.balancer == nil {
		conn, fallback, ok := s.group.penaltyFailoverDial(ctx, N.NetworkName(network), destination, outbound, err)
		if ok {
			s.logger.InfoContext(ctx, "lx penalty: failover to ", fallback.Tag())
		}
		return conn, ok
	}
	return s.rrHealDial(ctx, network, destination, outbound, err, picked)
}

// listenFailedLx takes a UDP open that failed: round_robin retries it once through
// the next live slot that takes UDP and starts a background check of the node
// (SPEC 138 §3.8); least_test has nothing more to do (penalties count TCP only).
func (s *URLTest) listenFailedLx(ctx context.Context, destination M.Socksaddr, outbound adapter.Outbound, err error, picked rrPick) (net.PacketConn, bool) {
	if s.balancer == nil {
		return nil, false
	}
	return s.rrHealListen(ctx, destination, outbound, err, picked)
}

// --- URLTestGroup ----------------------------------------------------------------

// closeLx runs under g.access at Close: the group's context is cancelled before
// the ticker early-return (050): a run started by PostStart can be in flight with
// no ticker armed, and that is exactly the run that used to survive shutdown. A
// dial racing Close cannot restart the ticker: upstream's closed flag, read by
// Touch under the lock, covers it since 37fffa325.
func (g *URLTestGroup) closeLx() {
	if g.cancel != nil {
		g.cancel()
	}
}

// probeGatedLx (SPEC 020): while the group itself is unreachable from the active
// routing tree (a selector switched away), probing would only wake idle-suspended
// members for nothing: the tick skips the cycle. idle_timeout still retires the
// ticker, and the next Touch (traffic returned) or the group becoming reachable
// again resumes probing.
func (g *URLTestGroup) probeGatedLx() bool {
	return g.reachability != nil && g.groupTag != "" && !g.reachability.OutboundReachable(g.groupTag)
}

// urlTestLx is the lx run of urlTest where it differs from upstream; handled is
// false for the upstream full run (testNodes + afterTestLx).
//
// A manual test (force) always tests everything (upstream force semantics since
// e4a4e8c79); for round_robin that also rebuilds the pool from the fresh results.
// The scheduled run:
//   - SPEC 125 — a quiet pause after a long tick run;
//   - SPEC 019 v2 — round_robin uses a lazy, pool-bounded health-check that tests no
//     more nodes than needed;
//   - SPEC 116 — failover probes only the held nodes; a failure escalates to a full run.
func (g *URLTestGroup) urlTestLx(ctx context.Context, force bool) (map[string]uint16, bool) {
	if force {
		if g.balancer != nil {
			g.balancer.rrRunBegin(false, manualRunLx(ctx)) // SPEC 019 — a forced run with no answer lays out nothing (afterTestLx); the manual test erases history
		}
		return nil, false
	}
	if g.tickTooSoon() {
		return make(map[string]uint16), true
	}
	if g.balancer != nil {
		return g.balancePool(ctx), true
	}
	if g.failover {
		return g.failoverCheck(ctx), true
	}
	return nil, false
}

// afterTestLx follows the full run of urlTest: round_robin rebuilds the pool from
// the fresh results when a probe of the run answered; the other modes update the selection, a forced run
// re-selecting in failover (SPEC 116).
func (g *URLTestGroup) afterTestLx(force bool) {
	if g.balancer != nil {
		if g.balancer.rrRunEnd() { // SPEC 019 — a run with no answer lays out nothing and lifts nothing
			g.balancer.unverified.clear() // the run probed every node with its answer as the control
			g.rebuildPool()
		}
		return
	}
	g.performSelectionUpdate(force) // urlTest notifies after it, as upstream
}

// testNodes runs the URL test over the given outbounds (skipping fresh history unless
// force), stores/deletes history when the batch ends, and returns tag->delay for the live ones: upstream's
// nested-group-aware batch with the probe hook (SPEC 054: an answer is proof of life
// and resets the node's penalties; SPEC 019: a node that answers fills a dead slot
// during the run). Shared by least_test and the round_robin pool paths. A round_robin
// batch with no answer erases no history, except in the manual test (SPEC 019,
// urltest_rr_run_lx.go).
func (g *URLTestGroup) testNodes(ctx context.Context, outbounds []adapter.Outbound, force bool) map[string]uint16 {
	batch := g.rrBatchBegin(outbounds)
	defer g.rrBatchEnd(batch)
	ctx = withProbeAlive(ctx, g.probeHook(batch))
	ctx = withProbeFailed(ctx, g.probeFailedHook(batch))
	if !force { // SPEC 125 — freshness from the previous tick run, not a fixed interval
		return g.testNodesTick(ctx, outbounds)
	}
	return URLTestOutbounds(ctx, g.outbound, g.history, g.logger, outbounds, g.link, g.interval, force)
}

// --- the probe hook of the batch -------------------------------------------------

type probeAliveKey struct{}

// withProbeAlive attaches the hook the URL test batch calls for every leaf that
// answered its probe, as soon as it answers; the batch writes the history only
// when it ends (upstream 37fffa325). It rides the context so the
// upstream URLTestOutbounds keeps its signature; a nested URLTest group's own run
// attaches its own hook.
func withProbeAlive(ctx context.Context, onAlive func(tag string)) context.Context {
	return context.WithValue(ctx, probeAliveKey{}, onAlive)
}

// probeAliveLx calls the hook of ctx, if any (SPEC 054, SPEC 019).
func probeAliveLx(ctx context.Context, tag string) {
	if onAlive, _ := ctx.Value(probeAliveKey{}).(func(tag string)); onAlive != nil {
		onAlive(tag)
	}
}

// lx:end urltest hooks
