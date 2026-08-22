package chain

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// typeHop — тип внутреннего outbound'а-хопа (виден только по тегу "<chain>#i").
const typeHop = "chain-hop"

var (
	_ adapter.Outbound          = (*hop)(nil)
	_ adapter.ChainLeafResolver = (*hop)(nil)
)

// hop — обёртка на позицию i. Единственная адресуемая по тегу сущность цепочки:
// цель detour звеньев позиции i+1 и цель URLTest «до позиции i».
type hop struct {
	chain   *Chain
	index   int
	target  adapter.Outbound
	isGroup bool
	errors  atomic.Int64
}

func newHop(chain *Chain, index int, target adapter.Outbound) *hop {
	_, isGroup := target.(adapter.OutboundGroup)
	return &hop{chain: chain, index: index, target: target, isGroup: isGroup}
}

func (h *hop) Type() string           { return typeHop }
func (h *hop) Tag() string            { return h.chain.hopTag(h.index) }
func (h *hop) Network() []string      { return h.target.Network() }
func (h *hop) Dependencies() []string { return []string{h.target.Tag()} }

func (h *hop) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var (
		conn net.Conn
		err  error
	)
	switch {
	case h.index == 0:
		// Вход: реальная сеть, цель как есть; привязка стирается, чтобы группа
		// на входе выбирала оригиналы.
		conn, err = h.target.DialContext(adapter.ContextWithoutChainHop(ctx), network, destination)
	case h.isGroup:
		conn, err = h.target.DialContext(adapter.ContextWithChainHop(ctx, h), network, destination)
	default:
		var resolved adapter.Outbound
		resolved, err = h.ResolveLeaf(ctx, h.target)
		if err == nil {
			conn, err = resolved.DialContext(ctx, network, destination)
		}
	}
	if err != nil {
		h.errors.Add(1)
		return nil, h.wrapError(err)
	}
	return conn, nil
}

func (h *hop) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	var (
		conn net.PacketConn
		err  error
	)
	switch {
	case h.index == 0:
		conn, err = h.target.ListenPacket(adapter.ContextWithoutChainHop(ctx), destination)
	case h.isGroup:
		conn, err = h.target.ListenPacket(adapter.ContextWithChainHop(ctx, h), destination)
	default:
		var resolved adapter.Outbound
		resolved, err = h.ResolveLeaf(ctx, h.target)
		if err == nil {
			conn, err = resolved.ListenPacket(ctx, destination)
		}
	}
	if err != nil {
		h.errors.Add(1)
		return nil, h.wrapError(err)
	}
	return conn, nil
}

// ResolveLeaf — выбранный узел позиции → его звено для этого хопа. direct на
// позиции ≥ 1 прозрачен (проход в хоп i−1), block терминален (как есть).
func (h *hop) ResolveLeaf(ctx context.Context, leaf adapter.Outbound) (adapter.Outbound, error) {
	if h.index == 0 {
		return leaf, nil
	}
	switch leaf.Type() {
	case C.TypeDirect:
		return &passthrough{hop: h.chain.hops[h.index-1], tag: leaf.Tag()}, nil
	case C.TypeBlock:
		return leaf, nil
	}
	cl, err := h.chain.cloneFor(h.index, leaf)
	if err != nil {
		return nil, err
	}
	return cl, nil
}

// wrapError локализует отказ: позиция, выбранный узел и через что шли.
func (h *hop) wrapError(err error) error {
	if h.index == 0 {
		return E.Cause(err, "chain[", h.chain.Tag(), "] #0 (", h.chain.resolvedLeafTag(0), ")")
	}
	return E.Cause(err, "chain[", h.chain.Tag(), "] #", h.index, " (", h.chain.resolvedLeafTag(h.index), ") via #", h.index-1, " (", h.chain.resolvedLeafTag(h.index-1), ")")
}

// passthrough — прозрачная позиция: выбранный direct означает «хопа нет»,
// дозвон уходит прямо в хоп i−1. Тег узла сохраняется для группы.
type passthrough struct {
	hop *hop
	tag string
}

func (p *passthrough) Type() string           { return C.TypeDirect }
func (p *passthrough) Tag() string            { return p.tag }
func (p *passthrough) Network() []string      { return p.hop.Network() }
func (p *passthrough) Dependencies() []string { return nil }

func (p *passthrough) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return p.hop.DialContext(ctx, network, destination)
}

func (p *passthrough) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return p.hop.ListenPacket(ctx, destination)
}
