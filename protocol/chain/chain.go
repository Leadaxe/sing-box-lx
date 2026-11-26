// Package chain — outbound `chain` (lx: SPEC 073 / 165, FEATURE 015).
//
// Позиции outbounds идут в порядке пакета: [0] — вход (касается реальной сети,
// используется как есть), последний — выход. Цепочка — перезапись `detour` при
// загрузке конфига (rewrite.go): каждый узел позиции i ≥ 1 получает detour на
// хоп "<chain>#<i-1>", хопы добавляются в общий список исходящих. В рантайме
// цепочка дозванивается через свой верхний хоп и держит выключение позиций
// (SPEC 075); копий узлов нет.
package chain

import (
	"context"
	"net"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// RegisterOutbound registers `chain` and the internal `chain-hop`, and the
// config rewrite that turns the former into the latter (SPEC 165).
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.ChainOutboundOptions](registry, C.TypeChain, NewOutbound)
	outbound.Register[option.ChainHopOutboundOptions](registry, C.TypeChainHop, NewHop)
	option.RegisterLXChainRewrite(Rewrite)
}

var (
	_ adapter.Outbound                = (*Chain)(nil)
	_ adapter.ChainPathProvider       = (*Chain)(nil)
	_ adapter.ChainStatusProvider     = (*Chain)(nil)
	_ adapter.ChainController         = (*Chain)(nil)
	_ adapter.ConnectionHandler       = (*Chain)(nil)
	_ adapter.PacketConnectionHandler = (*Chain)(nil)
)

type Chain struct {
	outbound.Adapter
	ctx        context.Context
	logger     log.ContextLogger
	outbound   adapter.OutboundManager
	connection adapter.ConnectionManager

	tags []string

	// SPEC 075: runtime position toggle. A disabled position >= 1 is a
	// passthrough to the hop below, a disabled entry dials the real network
	// through directDialer. The hops dial before the chain starts (a WG node
	// on a position handshakes at its own start), so the persisted set is
	// loaded on first use, not at Start.
	disabled          []atomic.Bool
	loadDisabledOnce  sync.Once
	directOnce        sync.Once
	directDialer      N.Dialer
	directErr         error
	interruptGroup    *interrupt.Group
	interruptExternal bool

	access sync.Mutex
	// state is set once by resolve; readers (status, path, dials) load it.
	state atomic.Pointer[chainState]

	stats chainStats
}

type chainState struct {
	targets []adapter.Outbound
	hops    []*Hop
}

type chainStats struct {
	dials  atomic.Int64
	errors atomic.Int64
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ChainOutboundOptions) (adapter.Outbound, error) {
	if len(options.Outbounds) < 2 {
		return nil, E.New("chain requires at least 2 outbounds")
	}
	return &Chain{
		// The chain depends on its top hop only: the hops carry the rest of
		// the start order (node → hop below → position).
		Adapter:           outbound.NewAdapter(C.TypeChain, tag, []string{N.NetworkTCP, N.NetworkUDP}, []string{HopTag(tag, len(options.Outbounds)-1)}),
		ctx:               ctx,
		logger:            logger,
		outbound:          service.FromContext[adapter.OutboundManager](ctx),
		connection:        service.FromContext[adapter.ConnectionManager](ctx),
		tags:              options.Outbounds,
		disabled:          make([]atomic.Bool, len(options.Outbounds)),
		interruptGroup:    interrupt.NewGroup(),
		interruptExternal: options.InterruptExistConnections,
	}, nil
}

func (c *Chain) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return c.resolve()
}

// resolve finds the positions and the hops; idempotent, called by the first
// hop that starts as well.
func (c *Chain) resolve() error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.state.Load() != nil {
		return nil
	}
	targets := make([]adapter.Outbound, len(c.tags))
	hops := make([]*Hop, len(c.tags))
	for i, tag := range c.tags {
		target, loaded := c.outbound.Outbound(tag)
		if !loaded {
			return E.New("position ", i, ": outbound not found: ", tag)
		}
		targets[i] = target
		hopOutbound, loaded := c.outbound.Outbound(HopTag(c.Tag(), i))
		if !loaded {
			return E.New("hop ", HopTag(c.Tag(), i), " not found: the config was not rewritten")
		}
		hop, isHop := hopOutbound.(*Hop)
		if !isHop {
			return E.New("tag ", HopTag(c.Tag(), i), " is not a chain hop")
		}
		hops[i] = hop
	}
	c.state.Store(&chainState{targets: targets, hops: hops})
	return nil
}

func (c *Chain) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := c.resolve(); err != nil {
		return nil, err
	}
	c.stats.dials.Add(1)
	c.traceChain(ctx) // SPEC 139
	hops := c.state.Load().hops
	conn, err := hops[len(hops)-1].DialContext(ctx, network, destination)
	if err != nil {
		c.stats.errors.Add(1)
		return nil, err
	}
	// SPEC 075: register the top-level connection so a position toggle can
	// interrupt it (selector semantics).
	return c.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (c *Chain) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if err := c.resolve(); err != nil {
		return nil, err
	}
	c.stats.dials.Add(1)
	c.traceChain(ctx) // SPEC 139
	hops := c.state.Load().hops
	conn, err := hops[len(hops)-1].ListenPacket(ctx, destination)
	if err != nil {
		c.stats.errors.Add(1)
		return nil, err
	}
	return c.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

// NewConnection / NewPacketConnection mark inbound (user) traffic as external
// for the interrupt group — without the mark every chain connection would be
// treated as internal and ripped on any toggle regardless of
// interrupt_exist_connections (SPEC 075, selector model).
func (c *Chain) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx, true)
	c.connection.NewConnection(ctx, c, conn, metadata, onClose)
}

func (c *Chain) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx, true)
	c.connection.NewPacketConnection(ctx, c, conn, metadata, onClose)
}

// resolvedLeaf — узел, который позиция выберет сейчас (через Now() групп), или
// nil, если группа без выбора.
func (c *Chain) resolvedLeaf(state *chainState, position int) adapter.Outbound {
	seen := make(map[string]bool)
	target := state.targets[position]
	for target != nil {
		group, isGroup := target.(adapter.OutboundGroup)
		if !isGroup {
			return target
		}
		now := adapter.GroupNow(group)
		if now == "" || seen[now] {
			return nil
		}
		seen[now] = true
		next, loaded := c.outbound.Outbound(now)
		if !loaded {
			return nil
		}
		target = next
	}
	return nil
}

func (c *Chain) resolvedLeafTag(position int) string {
	if state := c.state.Load(); state != nil {
		if leaf := c.resolvedLeaf(state, position); leaf != nil {
			return leaf.Tag()
		}
	}
	return c.tags[position]
}

// ChainPath — путь по непрозрачным позициям в порядке пакета.
func (c *Chain) ChainPath() []string {
	state := c.state.Load()
	if state == nil {
		return slices.Clone(c.tags)
	}
	path := make([]string, 0, len(c.tags))
	for i := range c.tags {
		// SPEC 075: a disabled position is not part of the traffic path.
		if c.positionDisabled(i) {
			continue
		}
		leaf := c.resolvedLeaf(state, i)
		if leaf == nil {
			path = append(path, c.tags[i])
			continue
		}
		if i > 0 && leaf.Type() == C.TypeDirect {
			continue
		}
		path = append(path, leaf.Tag())
	}
	return path
}

// --- SPEC 075: runtime position toggle ---------------------------------------

func (c *Chain) positionDisabled(position int) bool {
	c.loadDisabledOnce.Do(c.loadDisabled)
	return c.disabled[position].Load()
}

func (c *Chain) disabledStore() adapter.ChainDisabledStore {
	cacheFile := service.FromContext[adapter.CacheFile](c.ctx)
	if cacheFile == nil {
		return nil
	}
	store, _ := cacheFile.(adapter.ChainDisabledStore)
	return store
}

// loadDisabled applies the persisted disabled set (by position tag). A stored
// tag no longer present in the chain is ignored.
func (c *Chain) loadDisabled() {
	store := c.disabledStore()
	if store == nil {
		return
	}
	for _, tag := range store.LoadChainDisabled(c.Tag()) {
		if i := slices.Index(c.tags, tag); i >= 0 {
			c.disabled[i].Store(true)
			c.logger.Info("position ", i, " (", tag, ") disabled (restored from cache)")
		}
	}
}

func (c *Chain) persistDisabled() {
	store := c.disabledStore()
	if store == nil {
		return
	}
	var disabledTags []string
	for i := range c.tags {
		if c.disabled[i].Load() {
			disabledTags = append(disabledTags, c.tags[i])
		}
	}
	if err := store.StoreChainDisabled(c.Tag(), disabledTags); err != nil {
		c.logger.Error("store disabled positions: ", err)
	}
}

// SetPositionEnabled toggles one position (packet order, 0 = entry). Existing
// connections are interrupted with selector semantics: the internal ones —
// the sockets the nodes above keep through the hops — always, the user ones
// when interrupt_exist_connections is set.
func (c *Chain) SetPositionEnabled(position int, enabled bool) error {
	if position < 0 || position >= len(c.tags) {
		return E.New("position ", position, " out of range [0, ", len(c.tags)-1, "]")
	}
	c.loadDisabledOnce.Do(c.loadDisabled)
	if c.disabled[position].Swap(!enabled) == !enabled {
		return nil
	}
	if enabled {
		c.logger.Info("position ", position, " (", c.tags[position], ") enabled")
	} else {
		c.logger.Info("position ", position, " (", c.tags[position], ") disabled")
	}
	c.persistDisabled()
	c.interruptGroup.Interrupt(c.interruptExternal)
	return nil
}

// direct is the dialer of a disabled entry (Х5): a plain dialer with default
// options (interface binding / routing marks apply, like the `direct`
// outbound). Domain resolution needs a DNS transport manager; bare stands
// fall back to a non-resolving default dialer. Tests may pre-set it.
func (c *Chain) direct() (N.Dialer, error) {
	c.directOnce.Do(func() {
		if c.directDialer != nil {
			return
		}
		if service.FromContext[adapter.DNSTransportManager](c.ctx) != nil {
			c.directDialer, c.directErr = dialer.New(c.ctx, option.DialerOptions{}, true)
		} else {
			c.directDialer, c.directErr = dialer.NewDefault(c.ctx, option.DialerOptions{})
		}
	})
	return c.directDialer, c.directErr
}

func (c *Chain) ChainStatus() adapter.ChainStatus {
	status := adapter.ChainStatus{
		Tag:    c.Tag(),
		Dials:  c.stats.dials.Load(),
		Errors: c.stats.errors.Load(),
	}
	state := c.state.Load()
	for i := range c.tags {
		position := adapter.ChainPositionStatus{Tag: c.tags[i], Disabled: c.positionDisabled(i)}
		if state == nil {
			status.Positions = append(status.Positions, position)
			continue
		}
		_, position.IsGroup = state.targets[i].(adapter.OutboundGroup)
		position.Errors = state.hops[i].errors.Load()
		if leaf := c.resolvedLeaf(state, i); leaf != nil {
			position.Now = leaf.Tag()
			position.Transparent = i > 0 && leaf.Type() == C.TypeDirect
		}
		status.Positions = append(status.Positions, position)
	}
	return status
}
