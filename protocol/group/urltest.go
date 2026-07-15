package group

import (
	"context"
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

var _ adapter.OutboundGroup = (*URLTest)(nil)

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
	if balancer != nil && options.Tolerance != 0 {
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
	group.groupTag = s.Tag()    // lx: SPEC 020/007 — probe gating + AWG guard need the group's own tag
	group.passiveCheck = s.passiveCheck
	if s.balancer != nil {
		// lx: SPEC 020 — a pool rebuild changes the active routing tree; invalidate
		// the router's reachable cache. lx: SPEC 007 — and if the new pool contains
		// a WireGuard-based member, AWG consumers of this group must go down before
		// pick() can route them into it. ctx captured here has the invalidator.
		ctx := s.ctx
		outboundManager := s.outbound
		groupTag := s.Tag()
		balancer := s.balancer
		s.balancer.onChange = func() {
			invalidateReachability(ctx)
			suspendAmneziaWGConsumersOnPool(outboundManager, groupTag, balancer.poolTags())
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
		// History is keyed by RealTag(detour) (a nested-group member is tested and
		// stored under its live leaf, not the group tag); read it the same way, or
		// the slot's delay is always 0/dead for group members (SPEC 022 #5). Fall
		// back to the raw slot tag if the outbound can't be resolved.
		historyTag := tag
		if node, loaded := s.outbound.Outbound(tag); loaded {
			historyTag = RealTag(node)
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

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(true)
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
		case N.NetworkTCP:
			outbound = s.group.selectedOutboundTCP
		case N.NetworkUDP:
			outbound = s.group.selectedOutboundUDP
		default:
			return nil, E.Extend(N.ErrUnknownNetwork, network)
		}
		if outbound == nil {
			outbound, _ = s.group.Select(network)
		}
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		// lx: SPEC 019 passive_check — a successful TCP dial proves two-way
		// liveness of the node (the handshake traversed the whole chain).
		if s.passiveCheck && N.NetworkName(network) == N.NetworkTCP {
			s.group.markPassiveAlive(outbound.Tag())
		}
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	// lx: SPEC 019 v2 — in round_robin a dial error must NOT touch the pool: the cause is
	// unknown (dead node vs. dead destination vs. local network drop). Only the health-check
	// changes pool membership. least_test keeps the upstream behaviour (drop the history).
	if s.balancer == nil {
		s.group.history.DeleteURLTestHistory(outbound.Tag())
	}
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	if s.balancer != nil {
		outbound = s.selectBalanced(ctx, N.NetworkUDP, destination)
	} else {
		outbound = s.group.selectedOutboundUDP
		if outbound == nil {
			outbound, _ = s.group.Select(N.NetworkUDP)
		}
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.ListenPacket(ctx, destination)
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
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
	lastSelected                 common.TypedValue[string] // lx: SPEC 019 — Now() in balanced modes
	balancer                     *balancer                 // lx: SPEC 019 v2 — round_robin pool; nil for least_test
	groupTag                     string                    // lx: SPEC 020/007 — set by URLTest.Start (probe gating, AWG guard)
	reachability                 adapter.ReachabilityReporter
	// lx: SPEC 019 passive_check — tag → unix-nano of the last successful TCP
	// dial through that node. A fresh entry (< interval) is proof of two-way
	// liveness (the TCP handshake traversed the whole chain), letting the
	// health-check skip probing that node.
	passiveCheck bool
	passiveOK    sync.Map
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
	return &URLTestGroup{
		ctx:                          ctx,
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
	go g.CheckOutbounds(false)
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
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundTCP)); history != nil {
				minOutbound = g.selectedOutboundTCP
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundUDP)); history != nil {
				minOutbound = g.selectedOutboundUDP
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(detour))
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
		g.CheckOutbounds(false)
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
		g.CheckOutbounds(false)
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

func (g *URLTestGroup) CheckOutbounds(force bool) {
	_, _ = g.urlTest(g.ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	// lx: SPEC 019 v2 — a MANUAL test of a round_robin group must test everything
	// and rebuild the pool from fresh results (the lazy pool-bounded check is for
	// the interval ticker only). least_test keeps the upstream non-force semantics.
	return g.urlTest(ctx, g.balancer != nil)
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	result := make(map[string]uint16)
	if g.checking.Swap(true) {
		return result, nil
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
	if g.passiveCheck && !force && g.selectedPassivelyConfirmed() {
		return result, nil
	}
	result = g.testNodes(ctx, g.outbounds, force)
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
// and the round_robin force path.
func (g *URLTestGroup) testNodes(ctx context.Context, outbounds []adapter.Outbound, force bool) map[string]uint16 {
	result := make(map[string]uint16)
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	checked := make(map[string]bool)
	var resultAccess sync.Mutex
	for _, detour := range outbounds {
		tag := detour.Tag()
		realTag := RealTag(detour)
		if checked[realTag] {
			continue
		}
		history := g.history.LoadURLTestHistory(realTag)
		if !force && history != nil && time.Since(history.Time) < g.interval {
			continue
		}
		checked[realTag] = true
		p, loaded := g.outbound.Outbound(realTag)
		if !loaded {
			continue
		}
		b.Go(realTag, func() (any, error) {
			testCtx, cancel := context.WithTimeout(g.ctx, C.TCPTimeout)
			defer cancel()
			t, err := urltest.URLTest(testCtx, g.link, p)
			if err != nil {
				g.logger.Debug("outbound ", tag, " unavailable: ", err)
				g.history.DeleteURLTestHistory(realTag)
			} else {
				g.logger.Debug("outbound ", tag, " available: ", t, "ms")
				g.history.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
					Time:  time.Now(),
					Delay: t,
				})
				resultAccess.Lock()
				result[tag] = t
				resultAccess.Unlock()
			}
			return nil, nil
		})
	}
	b.Wait()
	return result
}

func (g *URLTestGroup) performUpdateCheck() {
	var updated bool
	var changed bool // lx: SPEC 020 — ANY selection change (incl. nil→first) re-shapes the active tree
	if outbound, exists := g.Select(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		if g.selectedOutboundTCP != nil {
			updated = true
		}
		if outbound != g.selectedOutboundTCP {
			changed = true
			// lx: SPEC 007 — the auto-switch may point the group at a WireGuard-based
			// member; AWG consumers of this group must go down BEFORE the switch commits.
			suspendAmneziaWGConsumersOnWireGuardSwitch(g.outbound, g.groupTag, outbound)
		}
		g.selectedOutboundTCP = outbound
	}
	if outbound, exists := g.Select(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		if g.selectedOutboundUDP != nil {
			updated = true
		}
		if outbound != g.selectedOutboundUDP {
			changed = true
			suspendAmneziaWGConsumersOnWireGuardSwitch(g.outbound, g.groupTag, outbound) // lx: SPEC 007
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
		if history := g.history.LoadURLTestHistory(RealTag(detour)); history != nil {
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
		if history := g.history.LoadURLTestHistory(RealTag(detour)); history != nil {
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
