package group

import (
	"context"
	"io"
	"maps"
	"net"
	"slices"
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
	_ adapter.Referrer                = (*URLTest)(nil)
)

type URLTest struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	group                        *URLTestGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
	lxURLTest                    // lx: SPEC 019, SPEC 116 — round_robin and failover state (urltest_hooks_lx.go)
}

func NewURLTest(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
	outbound := &URLTest{
		Adapter:                      outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		tolerance:                    options.Tolerance,
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if err := outbound.initLx(logger, options); err != nil { // lx: SPEC 019, SPEC 116 — the mode of the group
		return nil, err
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}
	for i, tag := range outbound.tags {
		if slices.Contains(outbound.tags[:i], tag) {
			return nil, E.New("duplicate tag: ", tag)
		}
	}
	return outbound, nil
}

func (s *URLTest) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateStart:
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
		s.startLx(group) // lx: SPEC 019, SPEC 020, 050, SPEC 116, SPEC 138 — the lx state of the group
		s.group = group
	case adapter.StartStateStarted:
		s.group.PostStart()
		scope.Add(s.group.Close)
	}
	return nil
}

func (s *URLTest) All() []string {
	return s.tags
}

func (s *URLTest) Selected(network string) adapter.Outbound {
	var outbound adapter.Outbound
	if network == N.NetworkUDP {
		outbound = s.selectedNowLx(network) // lx: SPEC 019 — the last pick in round_robin
	} else {
		outbound = s.selectedNowLx(network) // lx: SPEC 019 — the last pick in round_robin
	}
	if outbound == nil {
		outbound, _ = s.group.Select(network)
	}
	return outbound
}

func (s *URLTest) AttachConnection(closer io.Closer) func() {
	s.group.Touch()
	return s.group.interruptGroup.Add(closer, true)
}

func (s *URLTest) References() ([]string, []string) {
	group := s.group
	if group == nil {
		return nil, nil
	}
	var references []string
	selectedOutboundTCP := group.selectedOutboundTCP.Load()
	selectedOutboundUDP := group.selectedOutboundUDP.Load()
	if selectedOutboundTCP != nil {
		references = append(references, selectedOutboundTCP.Tag())
	}
	if selectedOutboundUDP != nil && selectedOutboundUDP != selectedOutboundTCP {
		references = append(references, selectedOutboundUDP.Tag())
	}
	references = append(references, s.poolReferences()...) // lx: SPEC 109
	return references, nil
}

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(withManualRunLx(s.ctx), true) // lx: SPEC 019 — the manual test (daemon RPC) erases history as upstream
}

func (s *URLTest) PerformUpdateCheck() {
	if s.group.balancer != nil { // lx: SPEC 019 — round_robin keeps no single selection (afterTestLx); the router calls this on every failed open
		return
	}
	if s.group.performUpdateCheck() {
		s.group.history.NotifyUpdated()
	}
}

func (s *URLTest) InterfaceUpdated(ctx context.Context) {
	s.rrNetworkChanged() // lx: SPEC 138 — a network change freezes the self-heal, run or not
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
		s.rrNetworkRun() // lx: SPEC 138 — the check in flight yields to the run
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *URLTest) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	var picked rrPick // lx: SPEC 138 — the round_robin pick: key, shift, start of the connection
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		outbound, picked = s.pickLx(ctx, network, destination) // lx: SPEC 019, SPEC 054, SPEC 139 — per-connection / penalty-aware pick
	case N.NetworkUDP:
		outbound, picked = s.pickLx(ctx, network, destination) // lx: SPEC 019, SPEC 054, SPEC 139 — per-connection / penalty-aware pick
	default:
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	if outbound == nil {
		outbound, _ = s.group.Select(network)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	outbound, dialCtx, err := s.dialStartLx(ctx, outbound, &picked) // lx: SPEC 073 — the chain leaf; SPEC 138 — a round_robin dial in flight is tracked
	if err != nil {
		return nil, err
	}
	conn, err := outbound.DialContext(dialCtx, network, destination) // lx: SPEC 138 — dialCtx
	if err == nil {
		conn = s.dialedLx(ctx, network, outbound, conn, picked) // lx: SPEC 054 — proof of life; SPEC 138 — round_robin watches for the first byte
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	if !E.IsClosedOrCanceled(err) {
		if s.group.deleteHistoryOnDialError(ctx, RealTag(outbound, N.NetworkName(network))) && !s.group.pathDead(outbound, err) { // lx: SPEC 019, SPEC 139 — round_robin and a probe keep the history; SPEC 054 — a dead path moves the selection itself, without Interrupt
			s.PerformUpdateCheck()
		}
	}
	if retryConn, ok := s.dialFailedLx(ctx, network, destination, outbound, err, picked); ok { // lx: SPEC 054, SPEC 138 — one second attempt
		return s.group.interruptGroup.NewConn(retryConn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound, picked := s.pickLx(ctx, N.NetworkUDP, destination) // lx: SPEC 019, SPEC 054, SPEC 138, SPEC 139 — per-connection / penalty-aware pick
	if outbound == nil {
		outbound, _ = s.group.Select(N.NetworkUDP)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	outbound, dialCtx, err := s.dialStartLx(ctx, outbound, &picked) // lx: SPEC 073 — the chain leaf; SPEC 138 — a round_robin open in flight is tracked
	if err != nil {
		return nil, err
	}
	conn, err := outbound.ListenPacket(dialCtx, destination) // lx: SPEC 138 — dialCtx
	if err == nil {
		conn = s.rrWatchPacket(ctx, outbound, conn, picked) // lx: SPEC 138 — round_robin watches for the first packet
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	if !E.IsClosedOrCanceled(err) {
		if s.group.deleteHistoryOnDialError(ctx, RealTag(outbound, N.NetworkUDP)) { // lx: SPEC 019, SPEC 139 — round_robin and a probe keep the history
			s.PerformUpdateCheck()
		}
	}
	if retryConn, ok := s.listenFailedLx(ctx, destination, outbound, err, picked); ok { // lx: SPEC 138 — one second attempt
		return s.group.interruptGroup.NewPacketConn(retryConn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	return nil, err
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
	selectedOutboundTCP          common.TypedValue[adapter.Outbound]
	selectedOutboundUDP          common.TypedValue[adapter.Outbound]
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	updateAccess                 sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      atomic.Bool
	closed                       atomic.Bool
	lastActive                   common.TypedValue[time.Time]
	lxURLTestGroup               // lx: SPEC 019, SPEC 020, 050, SPEC 054, SPEC 116, SPEC 125, SPEC 126 — lx state (urltest_hooks_lx.go)
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
	}, nil
}

func (g *URLTestGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started.Store(true)
	g.lastActive.Store(time.Now())
	g.seedPool() // lx: SPEC 019 — round_robin routes from the first connection, before the first health-check
	go g.CheckOutbounds(g.ctx, false)
}

func (g *URLTestGroup) Touch() {
	if !g.started.Load() {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.closed.Load() {
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
	g.closeLx() // lx: 050 — cancel an in-flight run
	if g.closed.Swap(true) {
		return nil
	}
	if g.ticker != nil {
		g.ticker.Stop()
		g.ticker = nil
		g.pause.UnregisterCallback(g.pauseCallback)
		g.pauseCallback = nil
	}
	close(g.close)
	return nil
}

func (g *URLTestGroup) Select(network string) (adapter.Outbound, bool) {
	var minDelay uint16
	var minOutbound adapter.Outbound
	var selectedOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		selectedOutbound = g.selectedOutboundTCP.Load()
	case N.NetworkUDP:
		selectedOutbound = g.selectedOutboundUDP.Load()
	}
	if selectedOutbound != nil {
		if history := g.history.LoadURLTestHistory(RealTag(selectedOutbound, network)); history != nil {
			minOutbound = selectedOutbound
			minDelay = history.Delay
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(detour, network))
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
		if g.probeGatedLx() { // lx: SPEC 020 — the group is out of the active routing tree
			continue
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

func (g *URLTestGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(withManualRunLx(ctx), true) // lx: SPEC 019 — the manual test erases history as upstream
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	defer g.checking.Store(false)
	if result, handled := g.urlTestLx(ctx, force); handled { // lx: SPEC 019, SPEC 116, SPEC 125 — the scheduled run of round_robin / failover, the tick pause
		return result, nil
	}
	result := g.testNodes(ctx, g.outbounds, force) // lx: SPEC 019, SPEC 054, SPEC 125 — the probe hook, the tick freshness
	g.afterTestLx(force)                           // lx: SPEC 019 — round_robin rebuilds the pool; SPEC 116 — a forced run re-selects in failover
	g.history.NotifyUpdated()
	return result, nil
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
	stored   map[string]*adapter.URLTestHistory
	deleted  []string
}

func URLTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	result := urlTestOutbounds(ctx, outboundManager, history, logger, outbounds, link, interval, force)
	history.NotifyUpdated()
	return result
}

func urlTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	b, ctx := batch.New(ctx, batch.WithConcurrencyNum[any](10)) // lx: 050 — the probes run on the batch context, which descends from the caller's
	testBatch := &urlTestBatch{
		ctx:      ctx,
		outbound: outboundManager,
		history:  history,
		logger:   logger,
		batch:    b,
		checked:  make(map[string]bool),
		result:   make(map[string]uint16),
		stored:   make(map[string]*adapter.URLTestHistory),
	}
	testBatch.test(outbounds, link, interval, force)
	b.Wait()
	history.UpdateURLTestHistory(testBatch.stored, testBatch.deleted)
	for _, outboundGroup := range testBatch.groups {
		groupHistory := history.LoadURLTestHistory(RealTag(outboundGroup, N.NetworkTCP))
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
					if b.ctx.Err() != nil {
						return nil, nil
					}
					b.logger.Debug("outbound ", tag, " unavailable: ", testResult.err)
					probeFailedLx(b.ctx, tag) // lx: SPEC 019 — a round_robin batch keeps what its own probe erases
					b.access.Lock()
					b.deleted = append(b.deleted, tag)
					b.access.Unlock()
				} else {
					b.logger.Debug("outbound ", tag, " available: ", testResult.delay, "ms")
					b.access.Lock()
					b.stored[tag] = &adapter.URLTestHistory{
						Time:  time.Now(),
						Delay: testResult.delay,
					}
					b.result[tag] = testResult.delay
					b.access.Unlock()
					probeAliveLx(b.ctx, tag) // lx: SPEC 054 — an answer is proof of life; SPEC 019 — a node that answers fills a dead slot
				}
				return nil, nil
			})
		}
	}
}

func (g *URLTestGroup) performUpdateCheck() bool {
	return g.performSelectionUpdate(false) // lx: SPEC 116
} // lx: SPEC 116

func (g *URLTestGroup) performSelectionUpdate(reselect bool) bool { // lx: SPEC 116 — upstream performUpdateCheck with the failover reselect flag
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	var (
		updated  bool
		selected bool
	)
	selectedOutboundTCP := g.selectedOutboundTCP.Load()
	if outbound, _ := g.selectForUpdate(N.NetworkTCP, reselect); outbound != nil && outbound != selectedOutboundTCP { // lx: SPEC 054, SPEC 116
		if selectedOutboundTCP != nil {
			updated = true
		}
		g.selectedOutboundTCP.Store(outbound)
		selected = true
	}
	selectedOutboundUDP := g.selectedOutboundUDP.Load()
	if outbound, _ := g.selectForUpdate(N.NetworkUDP, reselect); outbound != nil && outbound != selectedOutboundUDP { // lx: SPEC 054, SPEC 116
		if selectedOutboundUDP != nil {
			updated = true
		}
		g.selectedOutboundUDP.Store(outbound)
		selected = true
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
	if selected {
		invalidateReachability(g.ctx) // lx: SPEC 020 — every selection change re-shapes the active tree, the first one too
	}
	return selected
}
