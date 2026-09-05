package group

import (
	"context"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func RegisterURLTest(registry *outbound.Registry) {
	outbound.Register[option.URLTestOutboundOptions](registry, C.TypeURLTest, NewURLTest)
}

var (
	_ adapter.OutboundGroup           = (*URLTest)(nil)
	_ adapter.InterfaceUpdateListener = (*URLTest)(nil)
)

type URLTest struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	group                        *URLTestGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
	balancer                     *balancer // lx: SPEC 019 — nil for least_test (default)
	passiveCheck                 bool      // lx: SPEC 019 — skip probes for passively-confirmed nodes
}

func NewURLTest(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
	// lx: SPEC 019 v2 — round_robin balancer; nil balancer keeps legacy least_test behaviour.
	balancer, err := newBalancer(options)
	if err != nil {
		return nil, err
	}
	if warnLegacyTolerance(balancer, options) {
		logger.Warn("urltest: tolerance is ignored in round_robin mode; use balancer.pool_tolerance")
	}
	if balancer == nil && options.Balancer != nil {
		return nil, E.New("urltest: balancer is only valid with mode: round_robin")
	}
	if options.PassiveCheck && balancer != nil && balancer.poolTolerance > 0 {
		// pool_tolerance > 0 ranks ALL nodes by fresh delay every cycle — passive
		// liveness cannot substitute for a measurement there.
		logger.Warn("urltest: passive_check has no effect with balancer.pool_tolerance > 0 (that mode must measure every node)")
	}
	outbound := &URLTest{
		Adapter:                      outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		tolerance:                    options.Tolerance,
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
		balancer:                     balancer,
		passiveCheck:                 options.PassiveCheck,
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}
	return outbound, nil
}

func (s *URLTest) Start() error {
	outbounds := make([]adapter.Outbound, 0, len(s.tags))
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
	}
	group, err := NewURLTestGroup(s.ctx, s.outbound, s.logger, outbounds, s.link, s.interval, s.tolerance, s.idleTimeout, s.interruptExternalConnections)
	if err != nil {
		return err
	}
	group.balancer = s.balancer // lx: SPEC 019 v2 — health-check drives the pool through it
	group.groupTag = s.Tag()    // lx: SPEC 020 — probe gating needs the group's own tag
	group.passiveCheck = s.passiveCheck
	if s.balancer != nil {
		// lx: SPEC 020 — a pool rebuild changes the active routing tree; invalidate
		// the router's reachable cache. ctx captured here has the invalidator.
		ctx := s.ctx
		s.balancer.onChange = func() {
			invalidateReachability(ctx)
		}
	}
	s.group = group
	return nil
}

func (s *URLTest) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *URLTest) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *URLTest) Now() string {
	// lx: SPEC 019 — balanced modes have no single "current" node; report the last picked tag.
	if s.balancer != nil {
		return s.group.lastSelected.Load()
	}
	if s.group.selectedOutboundTCP != nil {
		return s.group.selectedOutboundTCP.Tag()
	} else if s.group.selectedOutboundUDP != nil {
		return s.group.selectedOutboundUDP.Tag()
	}
	// lx: SPEC 019 — cold start: before the first URL-test, selectedOutbound* is nil but
	// traffic already flows via the Select() fallback (outbounds[0] when no history yet).
	// Mirror exactly what the next DialContext would pick, so the UI shows the real node
	// instead of blank. Select() is the same source of truth DialContext uses.
	if outbound, _ := s.group.Select(N.NetworkTCP); outbound != nil {
		return outbound.Tag()
	}
	if outbound, _ := s.group.Select(N.NetworkUDP); outbound != nil {
		return outbound.Tag()
	}
	return ""
}

func (s *URLTest) All() []string {
	return s.tags
}

// PoolSlot is one entry of the round_robin rotation pool. lx: SPEC 019 v2.
type PoolSlot struct {
	Slot  int
	Tag   string
	Delay uint16 // ms; 0 = not measured / dead. A living node is clamped to >= 1 (see Pool).
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
		// History is keyed by RealTag(manager, detour) (a nested-group member is tested and
		// stored under its live leaf, not the group tag); read it the same way, or
		// the slot's delay is always 0/dead for group members (SPEC 022 #5). Fall
		// back to the raw slot tag if the outbound can't be resolved.
		historyTag := tag
		if node, loaded := s.outbound.Outbound(tag); loaded {
			historyTag = RealTag(s.outbound, node)
		}
		if history := s.group.history.LoadURLTestHistory(historyTag); history != nil {
			delay = history.Delay
			if delay == 0 {
				delay = 1 // live sub-ms node: never report 0 (0 is reserved for dead/untested)
			}
		}
		slots[i] = PoolSlot{Slot: i, Tag: tag, Delay: delay}
	}
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
	return C.URLTestModeLeastTest
}

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(s.ctx, true)
}

// selectBalanced picks an outbound per-connection in round_robin mode from the balancer's
// fixed-size pool. lx: SPEC 019 v2. fallback (Select's outbounds[0]) covers the cold-start
// window before the first health-check fills the pool. Returns nil only when nothing usable.
func (s *URLTest) selectBalanced(ctx context.Context, network string, destination M.Socksaddr) adapter.Outbound {
	fallback, _ := s.group.Select(network)
	selected := s.balancer.pick(ctx, destination, fallback, func(tag string) adapter.Outbound {
		node, _ := s.outbound.Outbound(tag)
		if node != nil && !common.Contains(node.Network(), network) {
			return nil
		}
		return node
	})
	if selected != nil {
		s.group.lastSelected.Store(selected.Tag())
	}
	return selected
}

func (s *URLTest) PerformUpdateCheck() {
	s.group.performUpdateCheck()
}

func (s *URLTest) InterfaceUpdated(ctx context.Context) {
	group := s.group
	if group == nil {
		return
	}
	if group.pause.IsDevicePaused() || group.pause.IsNetworkPaused() {
		return
	}
	go func() {
		s.checkAccess.Lock()
		defer s.checkAccess.Unlock()
		if ctx.Err() != nil {
			return
		}
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *URLTest) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	if s.balancer != nil {
		switch N.NetworkName(network) {
		case N.NetworkTCP, N.NetworkUDP:
			outbound = s.selectBalanced(ctx, network, destination)
		default:
			return nil, E.Extend(N.ErrUnknownNetwork, network)
		}
	} else {
		switch N.NetworkName(network) {
		case N.NetworkTCP, N.NetworkUDP:
			// lx: SPEC 054 — penalty-aware pick: аварийный режим обходит кеш
			// selectedOutbound*; обычный — апстримная семантика (кеш, иначе Select).
			outbound = s.group.pickForDial(N.NetworkName(network))
		default:
			return nil, E.Extend(N.ErrUnknownNetwork, network)
		}
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	// lx:begin chain
	// SPEC 073: внутри цепочки выбранный узел подменяется его звеном для хопа
	// (звено несёт тег оригинала — история/штрафы/passive-check ниже не меняются).
	outbound, err := adapter.ResolveChainLeaf(ctx, outbound)
	if err != nil {
		return nil, err
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	// lx:end chain
	if err == nil {
		// lx: SPEC 019 passive_check — a successful TCP dial proves two-way
		// liveness of the node (the handshake traversed the whole chain).
		if s.passiveCheck && N.NetworkName(network) == N.NetworkTCP {
			s.group.markPassiveAlive(outbound.Tag())
		}
		if s.balancer == nil {
			s.group.penaltyReset(RealTag(s.outbound, outbound)) // lx: SPEC 054 — успех = доказательство жизни
		}
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	// lx: SPEC 019 v2 — in round_robin a dial error must NOT touch the pool: the cause is
	// unknown (dead node vs. dead destination vs. local network drop). Only the health-check
	// changes pool membership. least_test keeps the upstream behaviour (drop the history).
	if s.balancer == nil {
		s.group.history.DeleteURLTestHistory(outbound.Tag())
		// lx: SPEC 054 — «путь мёртв» → штраф + один fallback-дайл; успех
		// переносит выбор группы на fallback (без Interrupt).
		if fbConn, fallback, ok := s.group.penaltyFailoverDial(ctx, N.NetworkName(network), destination, outbound, err); ok {
			s.logger.InfoContext(ctx, "lx penalty: failover to ", fallback.Tag())
			return s.group.interruptGroup.NewConn(fbConn, interrupt.IsExternalConnectionFromContext(ctx)), nil
		}
	}
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	if s.balancer != nil {
		outbound = s.selectBalanced(ctx, N.NetworkUDP, destination)
	} else {
		// lx: SPEC 054 — общий penalty-aware выбор (штрафы копит только TCP,
		// UDP ими пользуется).
		outbound = s.group.pickForDial(N.NetworkUDP)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	// lx:begin chain
	outbound, err := adapter.ResolveChainLeaf(ctx, outbound)
	if err != nil {
		return nil, err
	}
	conn, err := outbound.ListenPacket(ctx, destination)
	// lx:end chain
	if err == nil {
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	// lx: SPEC 019 v2 — round_robin dial error leaves the pool untouched (see DialContext).
	if s.balancer == nil {
		s.group.history.DeleteURLTestHistory(outbound.Tag())
	}
	return nil, err
}

func (s *URLTest) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *URLTest) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

type URLTestGroup struct {
	ctx                          context.Context
	cancel                       context.CancelFunc // lx: 050 — Close cancels an in-flight run
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selectedOutboundTCP          adapter.Outbound
	selectedOutboundUDP          adapter.Outbound
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	updateAccess                 sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
	lastSelected                 common.TypedValue[string] // lx: SPEC 019 — Now() in balanced modes
	balancer                     *balancer                 // lx: SPEC 019 v2 — round_robin pool; nil for least_test
	groupTag                     string                    // lx: SPEC 020 — set by URLTest.Start (probe gating)
	reachability                 adapter.ReachabilityReporter
	// lx: SPEC 019 passive_check — tag → unix-nano of the last successful TCP
	// dial through that node. A fresh entry (< interval) is proof of two-way
	// liveness (the TCP handshake traversed the whole chain), letting the
	// health-check skip probing that node.
	passiveCheck bool
	passiveOK    sync.Map
	// lx: SPEC 054 — penalty failover (least_test): tag → счётчик отказов «путь
	// мёртв»; сброс только доказательством жизни (успешный дайл / ответ на пробу).
	// forcedRetestRunning + lastForcedRetest — уровень-триггер аварийного
	// force-прогона с дельта-лимитом от КОНЦА прошлого прогона.
	penalties           sync.Map
	forcedRetestRunning atomic.Bool
	lastForcedRetest    common.TypedValue[time.Time]
}

func NewURLTestGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, tolerance uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*URLTestGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if tolerance == 0 {
		tolerance = 50
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	// lx: 050 — own the group's context so Close can cancel a run that is already
	// in flight. Without it a test blocked on a half-alive node kept its goroutine
	// and the whole outbound slice alive across box shutdown, piling up one
	// generation of zombies per restart.
	ctx, cancel := context.WithCancel(ctx)
	return &URLTestGroup{
		ctx:                          ctx,
		cancel:                       cancel,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		tolerance:                    tolerance,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
		reachability:                 service.FromContext[adapter.ReachabilityReporter](ctx), // lx: SPEC 020 — probe gating
	}, nil
}

func (g *URLTestGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	// lx: SPEC 019 v2 — seed the pool so round_robin can route from the first connection,
	// before the first health-check completes (history-warm nodes first, else config order).
	g.seedPool()
	go g.CheckOutbounds(g.ctx, false)
}

func (g *URLTestGroup) Touch() {
	g.access.Lock()
	defer g.access.Unlock()
	// lx: started is read/cleared under the lock (Close sets it false), so a dial
	// racing Close can no longer resurrect the ticker on a stopped group.
	if !g.started {
		return
	}
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *URLTestGroup) Close() error {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = false // lx: block Touch from restarting the ticker after Close
	// lx: 050 — cancel before the ticker early-return: a run started by PostStart
	// can be in flight with no ticker armed, and that is exactly the run that used
	// to survive shutdown.
	if g.cancel != nil {
		g.cancel()
	}
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	g.ticker = nil
	g.pause.UnregisterCallback(g.pauseCallback)
	g.pauseCallback = nil
	close(g.close)
	return nil
}

func (g *URLTestGroup) Select(network string) (adapter.Outbound, bool) {
	var minDelay uint16
	var minOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		if g.selectedOutboundTCP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.outbound, g.selectedOutboundTCP)); history != nil {
				minOutbound = g.selectedOutboundTCP
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.outbound, g.selectedOutboundUDP)); history != nil {
				minOutbound = g.selectedOutboundUDP
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
		if history == nil {
			continue
		}
		if minDelay == 0 || minDelay > history.Delay+g.tolerance {
			minDelay = history.Delay
			minOutbound = detour
		}
	}
	if minOutbound == nil {
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			return detour, false
		}
		return nil, false
	}
	return minOutbound, true
}

func (g *URLTestGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(g.ctx, false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		// lx: SPEC 020 — while the group itself is unreachable from the active
		// routing tree (a selector switched away), probing would only wake
		// idle-suspended members for nothing: skip the cycle. idle_timeout above
		// still retires the ticker, and the next Touch (traffic returned) or the
		// group becoming reachable again resumes probing.
		if g.reachability != nil && g.groupTag != "" && !g.reachability.OutboundReachable(g.groupTag) {
			continue
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

// selectedPassivelyConfirmed reports whether the least_test selection is
// passively proven alive: the TCP-selected node has a fresh passive signal, and
// the UDP selection (which gets no passive signal — ListenPacket has no
// handshake to confirm) is either absent or the very same node.
func (g *URLTestGroup) selectedPassivelyConfirmed() bool {
	if g.selectedOutboundTCP == nil || !g.passiveFresh(g.selectedOutboundTCP.Tag()) {
		return false
	}
	return g.selectedOutboundUDP == nil || g.selectedOutboundUDP == g.selectedOutboundTCP
}

// markPassiveAlive records a successful TCP dial through the node as passive
// proof of liveness. lx: SPEC 019 passive_check.
func (g *URLTestGroup) markPassiveAlive(tag string) {
	g.passiveOK.Store(tag, time.Now().UnixNano())
}

// passiveFresh reports whether the node has passive proof of liveness younger
// than the probe interval — recent enough to stand in for a URL probe.
func (g *URLTestGroup) passiveFresh(tag string) bool {
	if !g.passiveCheck {
		return false
	}
	value, ok := g.passiveOK.Load(tag)
	if !ok {
		return false
	}
	return time.Since(time.Unix(0, value.(int64))) < g.interval
}

func (g *URLTestGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	// A manual test always tests everything (upstream force semantics since
	// e4a4e8c79); lx: SPEC 019 v2 — for round_robin that also rebuilds the pool
	// from the fresh results (the lazy pool-bounded check is for the ticker only).
	return g.urlTest(ctx, true)
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	defer g.checking.Store(false)
	// lx: SPEC 019 v2 — round_robin uses a lazy, pool-bounded health-check that tests no more
	// nodes than needed (unless force, e.g. a manual URLTest, which always tests everything).
	if g.balancer != nil && !force {
		return g.balancePool(ctx), nil
	}
	// lx: SPEC 019 passive_check (least_test) — while the currently selected node
	// is passively confirmed alive (fresh successful TCP dial through it), skip
	// the whole periodic re-test cycle: nothing is broken, so don't wake N-1
	// suspended members with probes just to refresh delay numbers. A manual test
	// (force) always runs. Cost: history goes stale until the passive signal
	// lapses; the selection stays pinned to a working node — fewer switches.
	// lx: SPEC 054 — в аварийном режиме passive-skip отключён: рабочий запасной
	// пассивно подтверждается, циклы пропускались бы, и оштрафованный бывший
	// лучший никогда не получил бы пробу, которая сбрасывает его штрафы.
	if g.passiveCheck && !force && !g.penaltyEmergency(N.NetworkTCP) && g.selectedPassivelyConfirmed() {
		return make(map[string]uint16), nil
	}
	result := g.testNodes(ctx, g.outbounds, force)
	if g.balancer != nil {
		// force path (manual URLTest tested all nodes): rebuild the pool from fresh results.
		g.rebuildPool()
	} else {
		g.performUpdateCheck()
	}
	return result, nil
}

// testNodes runs the URL test over the given outbounds (skipping fresh history unless force),
// stores/deletes history, and returns tag->delay for the live ones. lx: shared by least_test
// and the round_robin pool paths; upstream's nested-group-aware batch (66beaf541) with the
// SPEC 054 hook: a probe answer is proof of life and resets the node's penalties.
func (g *URLTestGroup) testNodes(ctx context.Context, outbounds []adapter.Outbound, force bool) map[string]uint16 {
	return urlTestOutbounds(ctx, g.outbound, g.history, g.logger, outbounds, g.link, g.interval, force, g.penaltyReset)
}

type urlTestResult struct {
	delay uint16
	err   error
}

type urlTestBatch struct {
	ctx      context.Context
	outbound adapter.OutboundManager
	history  *urltest.HistoryStorage
	logger   log.Logger
	batch    *batch.Batch[any]
	checked  map[string]bool
	groups   []adapter.OutboundGroup
	access   sync.Mutex
	result   map[string]uint16
	onAlive  func(tag string) // lx: SPEC 054 — called for every leaf that answered the probe
}

func URLTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	return urlTestOutbounds(ctx, outboundManager, history, logger, outbounds, link, interval, force, nil)
}

func urlTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool, onAlive func(tag string)) map[string]uint16 {
	// lx: 050 — keep the per-node test context on the batch context: it descends
	// from the caller's ctx (and so from g.ctx, which Close cancels), so both the
	// batch's own cancellation and any deadline the caller passed in reach the probes.
	b, batchCtx := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	testBatch := &urlTestBatch{
		ctx:      batchCtx,
		outbound: outboundManager,
		history:  history,
		logger:   logger,
		batch:    b,
		checked:  make(map[string]bool),
		result:   make(map[string]uint16),
		onAlive:  onAlive,
	}
	testBatch.test(outbounds, link, interval, force)
	b.Wait()
	for _, outboundGroup := range testBatch.groups {
		groupHistory := history.LoadURLTestHistory(RealTag(outboundManager, outboundGroup))
		if groupHistory != nil {
			testBatch.result[outboundGroup.Tag()] = groupHistory.Delay
		}
	}
	return testBatch.result
}

func (b *urlTestBatch) test(outbounds []adapter.Outbound, link string, interval time.Duration, force bool) {
	for _, detour := range outbounds {
		tag := detour.Tag()
		if b.checked[tag] {
			continue
		}
		switch nested := detour.(type) {
		case *URLTest:
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.batch.Go(tag, func() (any, error) {
				nestedResult, _ := nested.group.urlTest(b.ctx, force)
				b.access.Lock()
				maps.Copy(b.result, nestedResult)
				b.access.Unlock()
				return nil, nil
			})
		case adapter.OutboundGroup:
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.test(common.FilterNotNil(common.Map(nested.All(), func(it string) adapter.Outbound {
				member, _ := b.outbound.Outbound(it)
				return member
			})), link, interval, force)
		default:
			history := b.history.LoadURLTestHistory(tag)
			if !force && history != nil && time.Since(history.Time) < interval {
				continue
			}
			b.checked[tag] = true
			b.batch.Go(tag, func() (any, error) {
				testCtx, cancel := context.WithTimeout(b.ctx, C.TCPTimeout)
				defer cancel()
				testChan := make(chan urlTestResult, 1)
				go func() {
					delay, testErr := urltest.URLTest(testCtx, link, detour)
					testChan <- urlTestResult{delay, testErr}
				}()
				var testResult urlTestResult
				select {
				case testResult = <-testChan:
				case <-testCtx.Done():
					testResult.err = testCtx.Err()
				}
				if testResult.err != nil {
					b.logger.Debug("outbound ", tag, " unavailable: ", testResult.err)
					b.history.DeleteURLTestHistory(tag)
				} else {
					b.logger.Debug("outbound ", tag, " available: ", testResult.delay, "ms")
					if b.onAlive != nil {
						b.onAlive(tag) // lx: SPEC 054 — ответ на пробу = доказательство жизни
					}
					b.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{
						Time:  time.Now(),
						Delay: testResult.delay,
					})
					b.access.Lock()
					b.result[tag] = testResult.delay
					b.access.Unlock()
				}
				return nil, nil
			})
		}
	}
}

func (g *URLTestGroup) performUpdateCheck() {
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	var updated bool
	var changed bool // lx: SPEC 020 — ANY selection change (incl. nil→first) re-shapes the active tree
	// lx: SPEC 054 — переизбор с учётом штрафов (в аварийном режиме — штрафы ↑, задержка ↑).
	if outbound, exists := g.selectPenaltyAware(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		if g.selectedOutboundTCP != nil {
			updated = true
		}
		if outbound != g.selectedOutboundTCP {
			changed = true
		}
		g.selectedOutboundTCP = outbound
	}
	if outbound, exists := g.selectPenaltyAware(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		if g.selectedOutboundUDP != nil {
			updated = true
		}
		if outbound != g.selectedOutboundUDP {
			changed = true
		}
		g.selectedOutboundUDP = outbound
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
	if changed {
		// lx: SPEC 020 — invalidate on the FIRST selection too: the nil→first
		// transition changes the active node just as much as a later switch, and a
		// cache computed from the cold-start fallback would otherwise stay stale
		// (wrong node held live / real node suspended) until the next auto-switch.
		invalidateReachability(g.ctx)
	}
}

// --- round_robin pool health-check (lx: SPEC 019 v2) --------------------------------

// poolSize is the effective pool size for the current node set: min(configured, available).
func (g *URLTestGroup) poolSize() int {
	size := g.balancer.poolSize
	if size > len(g.outbounds) {
		size = len(g.outbounds)
	}
	return size
}

// balancePool is the per-interval lazy health-check for round_robin. It tests no more nodes
// than needed to keep the pool full of live nodes, then applies the new slot occupancy.
// Returns tag->delay for every node it tested live (for the URLTest map / UI).
func (g *URLTestGroup) balancePool(ctx context.Context) map[string]uint16 {
	size := g.poolSize()
	if size == 0 {
		return map[string]uint16{}
	}
	if g.balancer.poolTolerance > 0 {
		return g.balancePoolTolerant(ctx, size)
	}
	return g.balancePoolFirstLive(ctx, size)
}

// balancePoolFirstLive (pool_tolerance == 0): re-test the nodes already in the pool, then —
// only if the pool is short of live nodes — walk the rest in config order, testing until the
// pool is full again. A dead pool node keeps its slot until a live replacement is found.
func (g *URLTestGroup) balancePoolFirstLive(ctx context.Context, size int) map[string]uint16 {
	current := g.balancer.poolTags()
	inPool := make(map[string]bool, len(current))
	for _, tag := range current {
		if tag != "" {
			inPool[tag] = true
		}
	}
	// 1. Re-test current pool members — except those passively confirmed alive
	// (lx: SPEC 019 passive_check — a fresh successful TCP dial through the slot
	// proves two-way liveness, no probe needed; with the option off passiveFresh
	// is always false). Collect which slots went dead.
	poolNodes := make([]adapter.Outbound, 0, len(current))
	passiveLive := make(map[string]bool, len(current))
	for _, node := range g.outboundsByTags(current) {
		if g.passiveFresh(node.Tag()) {
			passiveLive[node.Tag()] = true
			continue
		}
		poolNodes = append(poolNodes, node)
	}
	result := g.testNodes(ctx, poolNodes, true)
	liveTag := func(tag string) bool {
		if passiveLive[tag] {
			return true
		}
		_, ok := result[tag]
		return ok
	}

	// 2. Build the next occupancy IN PLACE: a live member keeps its exact slot index; a dead or
	// empty slot becomes "" (a hole to be refilled). Never compact — shifting a living node
	// across slots would move every sticky key bound to it (the SPEC invariant, see the file
	// header in urltest_balance_lx.go). next is at least `size` long so the pool can grow.
	slotCount := size
	if len(current) > slotCount {
		slotCount = len(current)
	}
	next := make([]string, slotCount)
	for i, tag := range current {
		if tag != "" && liveTag(tag) {
			next[i] = tag
		}
	}
	// emptySlot returns the first hole at/after `from`, or -1 when the pool is full.
	emptySlot := func(from int) int {
		for i := from; i < len(next); i++ {
			if next[i] == "" {
				return i
			}
		}
		return -1
	}
	// 2b. Refill holes (dead/empty slots) by writing replacements INTO the hole's own index:
	// walk non-pool nodes in config order, testing in batches of `size` (a full pool's worth)
	// in parallel, until no holes remain or nodes run out.
	if emptySlot(0) >= 0 {
		candidates := make([]adapter.Outbound, 0, len(g.outbounds))
		for _, detour := range g.outbounds {
			if !inPool[detour.Tag()] {
				candidates = append(candidates, detour)
			}
		}
		fill := 0
		for start := 0; start < len(candidates) && emptySlot(fill) >= 0; start += size {
			end := start + size
			if end > len(candidates) {
				end = len(candidates)
			}
			batch := candidates[start:end]
			tested := g.testNodes(ctx, batch, true)
			// Take live ones in config order (batch is already in config order).
			for _, detour := range batch {
				slot := emptySlot(fill)
				if slot < 0 {
					break
				}
				tag := detour.Tag()
				if delay, ok := tested[tag]; ok {
					next[slot] = tag
					fill = slot + 1
					result[tag] = delay
				}
			}
		}
	}
	// 3. Any hole left (not enough live nodes): put a dead member back in it so the pool never
	// shrinks. A dead occupant keeps the slot it already held when possible; otherwise the
	// remaining dead members fill the leftover holes (order does not matter — all are dead).
	if emptySlot(0) >= 0 {
		// Slots that still hold their original dead occupant: leave them be.
		for i, tag := range current {
			if i < len(next) && next[i] == "" && tag != "" && !liveTag(tag) {
				next[i] = tag
			}
		}
		// Surplus dead members (slots that no longer fit) drop into any leftover hole.
		placed := make(map[string]bool, len(next))
		for _, tag := range next {
			if tag != "" {
				placed[tag] = true
			}
		}
		for _, tag := range current {
			slot := emptySlot(0)
			if slot < 0 {
				break
			}
			if tag != "" && !liveTag(tag) && !placed[tag] {
				next[slot] = tag
				placed[tag] = true
			}
		}
	}
	g.balancer.setSlots(next)
	return result
}

// balancePoolTolerant (pool_tolerance > 0): test all nodes, then pick the top-`size` by delay,
// replacing a pool member only when an outside node beats it by more than the tolerance.
func (g *URLTestGroup) balancePoolTolerant(ctx context.Context, size int) map[string]uint16 {
	result := g.testNodes(ctx, g.outbounds, true)
	results := make(map[string]candidate, len(g.outbounds))
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		if delay, ok := result[tag]; ok {
			results[tag] = candidate{tag: tag, delay: delay, alive: true}
		} else {
			results[tag] = candidate{tag: tag, alive: false}
		}
	}
	next := planTolerantPool(g.balancer.poolTags(), results, size, g.balancer.poolTolerance)
	g.balancer.setSlots(next)
	return result
}

// rebuildPool re-derives slot occupancy after a forced full test (manual URLTest) from the
// history now present. It honours pool_tolerance: with tolerance == 0 it keeps living members
// in their slots (first-live discipline — a manual test must not reshuffle a stable pool and
// break sticky bindings); with tolerance > 0 it re-ranks by delay like the steady-state path.
func (g *URLTestGroup) rebuildPool() {
	size := g.poolSize()
	if size == 0 {
		return
	}
	results := make(map[string]candidate, len(g.outbounds))
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		if history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour)); history != nil {
			results[tag] = candidate{tag: tag, delay: history.Delay, alive: true}
		} else {
			results[tag] = candidate{tag: tag, alive: false}
		}
	}
	current := g.balancer.poolTags()
	if g.balancer.poolTolerance == 0 {
		live := make(map[string]bool, len(results))
		inPool := make(map[string]bool, len(current))
		for _, tag := range current {
			if tag != "" {
				inPool[tag] = true
			}
		}
		for tag, c := range results {
			if c.alive {
				live[tag] = true
			}
		}
		// Fill holes with live non-pool nodes, fastest first (history is warm after the force test).
		fillCandidates := make([]candidate, 0, len(results))
		for _, detour := range g.outbounds {
			tag := detour.Tag()
			if c, ok := results[tag]; ok && c.alive && !inPool[tag] {
				fillCandidates = append(fillCandidates, c)
			}
		}
		sortCandidatesByDelay(fillCandidates)
		fillOrder := make([]string, len(fillCandidates))
		for i, c := range fillCandidates {
			fillOrder[i] = c.tag
		}
		g.balancer.setSlots(planFirstLivePool(current, live, fillOrder, size))
		return
	}
	g.balancer.setSlots(planTolerantPool(current, results, size, g.balancer.poolTolerance))
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
		if history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour)); history != nil {
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
		tag := detour.Tag()
		if !seen[tag] {
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
