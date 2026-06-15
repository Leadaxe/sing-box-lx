package dialer

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type DirectDialer interface {
	IsEmpty() bool
}

type DetourDialer struct {
	outboundManager adapter.OutboundManager
	detour          string
	legacyDNSDialer bool
	dialer          N.Dialer
	initOnce        sync.Once
	initErr         error
	// lx:begin awg
	// ownerIsAmneziaWG marks the outbound that owns this dialer as an AmneziaWG
	// endpoint. With it set, init() rejects a detour into any WireGuard-based
	// endpoint (plain WireGuard or AmneziaWG): AWG traffic encapsulated inside a
	// WireGuard tunnel hangs the kernel on Android. See init().
	ownerIsAmneziaWG bool
	// lx:end awg
}

func NewDetour(outboundManager adapter.OutboundManager, detour string, legacyDNSDialer bool, ownerIsAmneziaWG bool) N.Dialer {
	return &DetourDialer{
		outboundManager: outboundManager,
		detour:          detour,
		legacyDNSDialer: legacyDNSDialer,
		// lx:begin awg
		ownerIsAmneziaWG: ownerIsAmneziaWG,
		// lx:end awg
	}
}

func InitializeDetour(dialer N.Dialer) error {
	detourDialer, isDetour := common.Cast[*DetourDialer](dialer)
	if !isDetour {
		return nil
	}
	return common.Error(detourDialer.Dialer())
}

func (d *DetourDialer) Dialer() (N.Dialer, error) {
	d.initOnce.Do(d.init)
	return d.dialer, d.initErr
}

func (d *DetourDialer) init() {
	dialer, loaded := d.outboundManager.Outbound(d.detour)
	if !loaded {
		d.initErr = E.New("outbound detour not found: ", d.detour)
		return
	}
	if !d.legacyDNSDialer {
		if directDialer, isDirect := dialer.(DirectDialer); isDirect {
			if directDialer.IsEmpty() {
				d.initErr = E.New("detour to an empty direct outbound makes no sense")
				return
			}
		}
	}
	// lx:begin awg
	// Reject AmneziaWG over WireGuard: when this dialer's owner is an AWG endpoint
	// and the detour target (directly, or any member of a selector/urltest group)
	// is a WireGuard-based endpoint — plain WireGuard or AmneziaWG — the AWG
	// traffic ends up encapsulated inside a WireGuard tunnel, which hangs the
	// kernel on Android. A detour into a non-WireGuard outbound (VLESS, Trojan,
	// direct, …) is fine and stays allowed. Behaves like the empty-direct guard
	// above: not a startup error, but every dial through this endpoint fails
	// here, so the rest of the config keeps working.
	if d.ownerIsAmneziaWG && detourTargetIsWireGuard(d.outboundManager, dialer, make(map[string]bool)) {
		d.initErr = E.New("amneziawg endpoint cannot detour through a wireguard-based endpoint (detour: ", d.detour, "): amneziawg over wireguard is not supported; use a non-wireguard detour (e.g. vless)")
		return
	}
	// lx:end awg
	d.dialer = dialer
}

// lx:begin awg
// detourTargetIsWireGuard reports whether outbound is — or, when it is a
// selector/urltest group, transitively contains — a WireGuard-based endpoint
// (type "wireguard", which covers both plain WireGuard and AmneziaWG). visited
// guards against cyclic group members.
func detourTargetIsWireGuard(outboundManager adapter.OutboundManager, outbound any, visited map[string]bool) bool {
	if typed, isTyped := outbound.(adapter.Outbound); isTyped && typed.Type() == C.TypeWireGuard {
		return true
	}
	group, isGroup := outbound.(adapter.OutboundGroup)
	if !isGroup {
		return false
	}
	for _, memberTag := range group.All() {
		if visited[memberTag] {
			continue
		}
		visited[memberTag] = true
		member, loaded := outboundManager.Outbound(memberTag)
		if !loaded {
			continue
		}
		if detourTargetIsWireGuard(outboundManager, member, visited) {
			return true
		}
	}
	return false
}

// lx:end awg

func (d *DetourDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	dialer, err := d.Dialer()
	if err != nil {
		return nil, err
	}
	return dialer.DialContext(ctx, network, destination)
}

func (d *DetourDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	dialer, err := d.Dialer()
	if err != nil {
		return nil, err
	}
	return dialer.ListenPacket(ctx, destination)
}

func (d *DetourDialer) Upstream() any {
	detour, _ := d.Dialer()
	return detour
}
