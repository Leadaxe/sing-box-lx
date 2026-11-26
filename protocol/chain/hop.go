package chain

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var _ adapter.Outbound = (*Hop)(nil)

// Hop is the internal outbound "<chain>#<i>" (SPEC 165, ALGORITHM §4.3): the
// detour of every node on position i+1 and the dial target of the chain on
// its top position. It never copies a node: a node on its position dials as
// itself, its own detour already being the hop below.
type Hop struct {
	outbound.Adapter
	manager adapter.OutboundManager
	options option.ChainHopOutboundOptions

	resolveOnce sync.Once
	resolveErr  error
	chain       *Chain
	target      adapter.Outbound
	below       *Hop
	// isGroup: the position is a group; its pick dials through its own
	// detour, a picked `direct` through the mark (Х8).
	isGroup bool
	errors  atomic.Int64
}

func NewHop(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ChainHopOutboundOptions) (adapter.Outbound, error) {
	if options.Chain == "" || options.Outbound == "" {
		return nil, E.New(C.TypeChainHop, " is internal and cannot be used in a config")
	}
	dependencies := []string{options.Outbound}
	if options.Below != "" {
		dependencies = append(dependencies, options.Below)
	}
	return &Hop{
		Adapter: outbound.NewAdapter(C.TypeChainHop, tag, []string{N.NetworkTCP, N.NetworkUDP}, dependencies),
		manager: service.FromContext[adapter.OutboundManager](ctx),
		options: options,
	}, nil
}

func (h *Hop) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.resolve()
}

// resolve finds the chain, the position and the hop below once: at Start, or
// at the first dial if a node above dials before the hop has started.
func (h *Hop) resolve() error {
	h.resolveOnce.Do(func() { h.resolveErr = h.doResolve() })
	return h.resolveErr
}

func (h *Hop) doResolve() error {
	chainOutbound, loaded := h.manager.Outbound(h.options.Chain)
	if !loaded {
		return E.New("chain not found: ", h.options.Chain)
	}
	chain, isChain := chainOutbound.(*Chain)
	if !isChain {
		return E.New(h.options.Chain, " is not a chain")
	}
	if err := chain.resolve(); err != nil {
		return E.Cause(err, "chain ", chain.Tag())
	}
	state := chain.state.Load()
	h.chain = chain
	h.target = state.targets[h.options.Position]
	_, h.isGroup = h.target.(adapter.OutboundGroup)
	if h.options.Position > 0 {
		h.below = state.hops[h.options.Position-1]
	}
	return nil
}

// dialTarget is the outbound this dial goes to and the context it goes with
// (Х1–Х6). nil outbound with a nil error: the disabled entry, direct dialer.
func (h *Hop) dialTarget(ctx context.Context) (context.Context, adapter.Outbound) {
	index := h.options.Position
	if h.chain.positionDisabled(index) {
		if h.below != nil {
			return ctx, h.below // Х4
		}
		return adapter.ContextWithChainDirect(ctx, nil), nil // Х5
	}
	if index > 0 && h.target.Type() == C.TypeDirect {
		return ctx, h.below // Х3
	}
	if h.isGroup && h.below != nil {
		// Х8: a direct the group picks goes to the hop below; the hook
		// clears the mark once a node is picked.
		ctx = adapter.ContextWithChainDirect(ctx, h.below)
	} else {
		// A node dials as itself: its own dials (DNS of its server) are not
		// the chain's.
		ctx = adapter.ContextWithChainDirect(ctx, nil)
	}
	if !h.isGroup {
		adapter.RecordCarrier(ctx, h.target) // Х7: the node of the position carries the dial
	}
	return ctx, h.target
}

func (h *Hop) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := h.resolve(); err != nil {
		return nil, err
	}
	ctx, target := h.dialTarget(ctx)
	var (
		conn net.Conn
		err  error
	)
	if target == nil {
		var direct N.Dialer
		direct, err = h.chain.direct()
		if err == nil {
			conn, err = direct.DialContext(ctx, network, destination)
		}
	} else {
		conn, err = target.DialContext(ctx, network, destination)
	}
	if err != nil {
		h.errors.Add(1)
		return nil, h.wrapError(err, target == nil)
	}
	return h.chain.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (h *Hop) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if err := h.resolve(); err != nil {
		return nil, err
	}
	ctx, target := h.dialTarget(ctx)
	var (
		conn net.PacketConn
		err  error
	)
	if target == nil {
		var direct N.Dialer
		direct, err = h.chain.direct()
		if err == nil {
			conn, err = direct.ListenPacket(ctx, destination)
		}
	} else {
		conn, err = target.ListenPacket(ctx, destination)
	}
	if err != nil {
		h.errors.Add(1)
		return nil, h.wrapError(err, target == nil)
	}
	return h.chain.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

// wrapError localises a failure: position, the node picked there and what
// was below. A hop passing through (disabled, direct) adds nothing: the hop
// below names itself.
func (h *Hop) wrapError(err error, directFallback bool) error {
	c := h.chain
	index := h.options.Position
	switch {
	case directFallback:
		return E.Cause(err, "chain[", c.Tag(), "] #0 (direct fallback)")
	case c.positionDisabled(index), index > 0 && h.target.Type() == C.TypeDirect:
		return err
	case index == 0:
		return E.Cause(err, "chain[", c.Tag(), "] #0 (", c.resolvedLeafTag(0), ")")
	}
	return E.Cause(err, "chain[", c.Tag(), "] #", index, " (", c.resolvedLeafTag(index), ") via #", index-1, " (", c.resolvedLeafTag(index-1), ")")
}
