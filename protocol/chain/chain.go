// Package chain — outbound `chain` (lx: SPEC 073 / FEATURE 015): виртуальная
// цепочка хопов из групп и узлов.
//
// Позиции outbounds идут в порядке пакета: [0] — вход (касается реальной сети,
// используется как есть), последний — выход. На каждую позицию есть хоп —
// внутренний outbound с тегом "<chain>#<i>", единственная сущность, адресуемая
// по тегу. Группы не копируются: хоп зовёт DialContext оригинальной группы с
// контекстной привязкой, а хук adapter.ResolveChainLeaf в точках дозвона групп
// подменяет выбранный узел на его звено — рантайм-экземпляр узла с detour на
// хоп i−1, созданный штатным реестром из преобразованной копии опций.
package chain

import (
	"context"
	"net"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	defaultIdleTimeout = 5 * time.Minute
	minEvictTick       = 15 * time.Second
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.ChainOutboundOptions](registry, C.TypeChain, NewOutbound)
}

var (
	_ adapter.Outbound            = (*Chain)(nil)
	_ adapter.ChainPathProvider   = (*Chain)(nil)
	_ adapter.ChainStatusProvider = (*Chain)(nil)
	_ adapter.EndpointCloneHolder = (*Chain)(nil)
)

type Chain struct {
	outbound.Adapter
	ctx        context.Context
	router     adapter.Router
	logger     log.ContextLogger
	logFactory log.Factory
	outbound   adapter.OutboundManager
	endpoint   adapter.EndpointManager

	tags        []string
	idleTimeout time.Duration
	strip       stripSet
	rewrite     map[string]any

	targets []adapter.Outbound
	hops    []*hop

	cloneMu  sync.Mutex
	clones   map[cloneKey]*clone
	inflight map[cloneKey]*cloneCall
	closed   bool
	stopCh   chan struct{}
	stopOnce sync.Once

	stats chainStats
}

type chainStats struct {
	dials         atomic.Int64
	errors        atomic.Int64
	clonesCreated atomic.Int64
	clonesEvicted atomic.Int64
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ChainOutboundOptions) (adapter.Outbound, error) {
	if len(options.Outbounds) < 2 {
		return nil, E.New("chain requires at least 2 outbounds")
	}
	seen := make(map[string]struct{}, len(options.Outbounds))
	for i, position := range options.Outbounds {
		if position == "" {
			return nil, E.New("empty outbound tag at position ", i)
		}
		if position == tag {
			return nil, E.New("chain cannot contain itself: position ", i)
		}
		if _, duplicate := seen[position]; duplicate {
			return nil, E.New("duplicate outbound in chain: ", position, " (position ", i, ")")
		}
		seen[position] = struct{}{}
	}
	strip, err := buildStripSet(options.StripEvasionEnabled(), options.Strip)
	if err != nil {
		return nil, err
	}
	for typeName := range options.Rewrite {
		if typeName == "" {
			return nil, E.New("rewrite: empty outbound type")
		}
	}
	idleTimeout := time.Duration(options.IdleTimeout)
	if options.IdleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	} else if idleTimeout < 0 {
		idleTimeout = 0
	}
	return &Chain{
		Adapter:     outbound.NewAdapter(C.TypeChain, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:         ctx,
		router:      router,
		logger:      logger,
		logFactory:  service.FromContext[log.Factory](ctx),
		outbound:    service.FromContext[adapter.OutboundManager](ctx),
		endpoint:    service.FromContext[adapter.EndpointManager](ctx),
		tags:        options.Outbounds,
		idleTimeout: idleTimeout,
		strip:       strip,
		rewrite:     options.Rewrite,
		clones:      make(map[cloneKey]*clone),
		inflight:    make(map[cloneKey]*cloneCall),
		stopCh:      make(chan struct{}),
	}, nil
}

// hopTag — формат "<chain>#<i>". ПУБЛИЧНЫЙ КОНТРАКТ (SPEC 073): клиенты
// (лаунчер, LxBox) адресуют по этим тегам пробы позиций (urltest через
// CommandClient). Молчаливой смены формата не будет: любое изменение — только
// явной правкой SPEC 073 с миграционной заметкой в changelog.
func (c *Chain) hopTag(index int) string {
	return c.Tag() + "#" + strconv.Itoa(index)
}

// Start: резолв позиций, валидация типов и патчей по всем достижимым узлам,
// регистрация хопов, прогрев детерминированных позиций, тикер эвикшна.
func (c *Chain) Start() error {
	registrar, ok := c.outbound.(adapter.InternalOutboundRegistrar)
	if !ok {
		return E.New("outbound manager does not support chain hops")
	}
	c.targets = make([]adapter.Outbound, len(c.tags))
	for i, tag := range c.tags {
		target, loaded := c.outbound.Outbound(tag)
		if !loaded {
			return E.New("position ", i, ": outbound not found: ", tag)
		}
		c.targets[i] = target
	}
	for i := range c.tags {
		if _, exists := c.outbound.Outbound(c.hopTag(i)); exists {
			return E.New("tag ", c.hopTag(i), " is reserved for chain hop but already exists")
		}
	}
	// Типы узлов на позициях ≥ 1 и сухой прогон strip/rewrite/MTU по каждому.
	for i := 1; i < len(c.tags); i++ {
		leaves, err := c.leavesOf(c.targets[i])
		if err != nil {
			return E.Cause(err, "position ", i)
		}
		for _, leaf := range leaves {
			if err := c.validateLeafType(i, leaf); err != nil {
				return err
			}
			if !isClonable(leaf) {
				continue
			}
			if _, err := c.buildCloneOptions(i, leaf); err != nil {
				return E.Cause(err, "position ", i, " (", leaf.Tag(), "): dry-run")
			}
		}
	}
	c.hops = make([]*hop, len(c.tags))
	for i := range c.tags {
		c.hops[i] = newHop(c, i, c.targets[i])
	}
	for i, h := range c.hops {
		if err := registrar.AddInternal(h); err != nil {
			for j := 0; j < i; j++ {
				registrar.RemoveInternal(c.hops[j].Tag())
			}
			return E.Cause(err, "register hop ", i)
		}
	}
	if err := c.preload(); err != nil {
		c.Close()
		return err
	}
	if c.idleTimeout > 0 {
		go c.evictLoop()
	}
	return nil
}

// preload поднимает звенья позиций, чей выбор детерминирован на старте:
// узлы и селекторы любой вложенности; urltest на пути выбора — стоп (у него на
// старте нет замеров, Now() случаен). Ошибка прогрева = ошибка старта.
func (c *Chain) preload() error {
	for i := 1; i < len(c.tags); i++ {
		leaf := deterministicLeaf(c.outbound, c.targets[i])
		if leaf == nil || !isClonable(leaf) {
			continue
		}
		if _, err := c.cloneFor(i, leaf); err != nil {
			return E.Cause(err, "preload position ", i, " (", leaf.Tag(), ")")
		}
	}
	return nil
}

// deterministicLeaf: лист → он сам; selector → рекурсивно по Now(); urltest и
// прочие группы → nil.
func deterministicLeaf(manager adapter.OutboundManager, target adapter.Outbound) adapter.Outbound {
	seen := make(map[string]bool)
	for target != nil {
		group, isGroup := target.(adapter.OutboundGroup)
		if !isGroup {
			return target
		}
		if target.Type() != C.TypeSelector {
			return nil
		}
		now := group.Now()
		if now == "" || seen[now] {
			return nil
		}
		seen[now] = true
		next, loaded := manager.Outbound(now)
		if !loaded {
			return nil
		}
		target = next
	}
	return nil
}

// leavesOf — все узлы, достижимые из target через группы любой вложенности.
func (c *Chain) leavesOf(target adapter.Outbound) ([]adapter.Outbound, error) {
	var leaves []adapter.Outbound
	seen := make(map[string]bool)
	var walk func(current adapter.Outbound) error
	walk = func(current adapter.Outbound) error {
		if seen[current.Tag()] {
			return nil
		}
		seen[current.Tag()] = true
		group, isGroup := current.(adapter.OutboundGroup)
		if !isGroup {
			leaves = append(leaves, current)
			return nil
		}
		for _, memberTag := range group.All() {
			member, loaded := c.outbound.Outbound(memberTag)
			if !loaded {
				return E.New("group ", current.Tag(), ": member not found: ", memberTag)
			}
			if err := walk(member); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(target); err != nil {
		return nil, err
	}
	return leaves, nil
}

// validateLeafType: позиция ≥ 1 допускает узлы с dial-полями, direct (прозрачный),
// block (терминальный). Вложенный chain и типы без dial-полей — отказ.
func (c *Chain) validateLeafType(position int, leaf adapter.Outbound) error {
	switch leaf.Type() {
	case C.TypeDirect, C.TypeBlock:
		return nil
	case C.TypeChain:
		return E.New("position ", position, " (", leaf.Tag(), "): nested chain is only allowed at position 0")
	}
	typeName, options, err := c.optionsOf(leaf)
	if err != nil {
		return E.Cause(err, "position ", position, " (", leaf.Tag(), ")")
	}
	if _, hasDialer := options.(option.DialerOptionsWrapper); !hasDialer {
		return E.New("position ", position, " (", leaf.Tag(), ", ", typeName, "): type has no dial fields and cannot be a chain link")
	}
	return nil
}

func isClonable(leaf adapter.Outbound) bool {
	switch leaf.Type() {
	case C.TypeDirect, C.TypeBlock:
		return false
	}
	return true
}

// optionsOf — тип и опции узла из того менеджера, где он живёт.
func (c *Chain) optionsOf(leaf adapter.Outbound) (string, any, error) {
	if provider, ok := c.outbound.(adapter.OutboundOptionsProvider); ok {
		if typeName, options, loaded := provider.OptionsOf(leaf.Tag()); loaded {
			return typeName, options, nil
		}
	}
	if provider, ok := c.endpoint.(adapter.EndpointOptionsProvider); ok {
		if typeName, options, loaded := provider.OptionsOf(leaf.Tag()); loaded {
			return typeName, options, nil
		}
	}
	return "", nil, E.New("options of ", leaf.Tag(), " are not available")
}

func (c *Chain) isEndpointLeaf(leaf adapter.Outbound) bool {
	if c.endpoint == nil {
		return false
	}
	_, loaded := c.endpoint.Get(leaf.Tag())
	return loaded
}

func (c *Chain) Close() error {
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.cloneMu.Lock()
	c.closed = true
	clones := make([]*clone, 0, len(c.clones))
	for key, cl := range c.clones {
		clones = append(clones, cl)
		delete(c.clones, key)
	}
	c.cloneMu.Unlock()
	// Верхние звенья держат соединения через нижние — закрываем от выхода ко входу.
	sort.Slice(clones, func(i, j int) bool { return clones[i].position > clones[j].position })
	var err error
	for _, cl := range clones {
		err = E.Append(err, cl.close(), func(err error) error {
			return E.Cause(err, "close clone ", cl.key.leafTag, "@", cl.position)
		})
	}
	if registrar, ok := c.outbound.(adapter.InternalOutboundRegistrar); ok {
		for _, h := range c.hops {
			registrar.RemoveInternal(h.Tag())
		}
	}
	return err
}

func (c *Chain) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	c.stats.dials.Add(1)
	conn, err := c.hops[len(c.hops)-1].DialContext(ctx, network, destination)
	if err != nil {
		c.stats.errors.Add(1)
		return nil, err
	}
	return conn, nil
}

func (c *Chain) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	c.stats.dials.Add(1)
	conn, err := c.hops[len(c.hops)-1].ListenPacket(ctx, destination)
	if err != nil {
		c.stats.errors.Add(1)
		return nil, err
	}
	return conn, nil
}

// resolvedLeaf — узел, который позиция выберет сейчас (через Now() групп), или
// nil, если группа без выбора.
func (c *Chain) resolvedLeaf(position int) adapter.Outbound {
	seen := make(map[string]bool)
	target := c.targets[position]
	for target != nil {
		group, isGroup := target.(adapter.OutboundGroup)
		if !isGroup {
			return target
		}
		now := group.Now()
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
	if leaf := c.resolvedLeaf(position); leaf != nil {
		return leaf.Tag()
	}
	return c.tags[position]
}

// ChainPath — путь по непрозрачным позициям в порядке пакета.
func (c *Chain) ChainPath() []string {
	if c.targets == nil {
		return slices.Clone(c.tags)
	}
	path := make([]string, 0, len(c.tags))
	for i := range c.tags {
		leaf := c.resolvedLeaf(i)
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

// CloneEndpoints — живые звенья-endpoint'ы (для ENERGY / смены сети).
func (c *Chain) CloneEndpoints() []adapter.Endpoint {
	c.cloneMu.Lock()
	defer c.cloneMu.Unlock()
	var endpoints []adapter.Endpoint
	for _, cl := range c.clones {
		if endpoint, ok := cl.inner.(adapter.Endpoint); ok {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

func (c *Chain) ChainStatus() adapter.ChainStatus {
	status := adapter.ChainStatus{
		Tag:           c.Tag(),
		Dials:         c.stats.dials.Load(),
		Errors:        c.stats.errors.Load(),
		ClonesCreated: c.stats.clonesCreated.Load(),
		ClonesEvicted: c.stats.clonesEvicted.Load(),
	}
	c.cloneMu.Lock()
	status.LiveClones = int64(len(c.clones))
	clonesByPosition := make(map[int]map[string]*clone)
	for key, cl := range c.clones {
		if clonesByPosition[key.position] == nil {
			clonesByPosition[key.position] = make(map[string]*clone)
		}
		clonesByPosition[key.position][key.leafTag] = cl
	}
	inflight := make(map[cloneKey]bool, len(c.inflight))
	for key := range c.inflight {
		inflight[key] = true
	}
	c.cloneMu.Unlock()
	for i := range c.tags {
		position := adapter.ChainPositionStatus{Tag: c.tags[i]}
		if c.targets == nil {
			status.Positions = append(status.Positions, position)
			continue
		}
		_, position.IsGroup = c.targets[i].(adapter.OutboundGroup)
		if c.hops != nil {
			position.Errors = c.hops[i].errors.Load()
		}
		leaf := c.resolvedLeaf(i)
		if leaf != nil {
			position.Now = leaf.Tag()
			position.Transparent = i > 0 && leaf.Type() == C.TypeDirect
			if i > 0 {
				if cl := clonesByPosition[i][leaf.Tag()]; cl != nil {
					position.Clone = cl.status()
				} else if inflight[cloneKey{position: i, leafTag: leaf.Tag()}] {
					position.Clone = &adapter.ChainCloneStatus{State: "starting"}
				}
			}
		}
		status.Positions = append(status.Positions, position)
	}
	return status
}
