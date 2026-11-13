package outbound

// lx: SPEC 060 — the outbound wrapper of lx.sniff_guard.
//
// A node the guard covers is wrapped at creation: every DialContext and
// ListenPacket runs inside Node.Dial, which may run the inner call a second
// time with the opposite decision. Each attempt gets a fresh CarrierRecord so
// the handshake layer can tell which path this very attempt took.
//
// The rest of the core inspects outbounds by interface (Lifecycle, Endpoint,
// InterfaceUpdateListener, IdleConnectionKeeper, OutboundWithMultiplex,
// OutboundGroup, FlowOutbound, ...). The wrapper therefore exposes exactly
// the method set of the inner outbound: a variant is picked by the optional
// interfaces the inner one implements, and the result is checked by
// reflection against the inner type. An outbound for which no variant
// matches exactly, and every group, is returned unwrapped.

import (
	"context"
	"io"
	"net"
	"reflect"
	"sort"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

// wrapSniffGuardLx returns the constructor result filter of the outbound
// tagged tag: the outbound wrapped when the guard covers the tag, otherwise
// as it is.
func wrapSniffGuardLx(ctx context.Context, logger log.ContextLogger, tag string) func(adapter.Outbound, error) (adapter.Outbound, error) {
	return func(outbound adapter.Outbound, err error) (adapter.Outbound, error) {
		if err != nil || outbound == nil {
			return outbound, err
		}
		node := lxsniffguard.FromContext(ctx).Node(tag)
		if node == nil {
			return outbound, nil
		}
		wrapped := newSniffGuardOutbound(outbound, node)
		if wrapped == nil {
			logger.Warn("sniff guard: outbound/", outbound.Type(), "[", tag, "] is left unguarded: the wrapper cannot preserve its interfaces")
			return outbound, nil
		}
		return wrapped, nil
	}
}

// sniffGuardOutbound overrides the two dial methods; everything else of
// adapter.Outbound is the inner outbound's.
type sniffGuardOutbound struct {
	adapter.Outbound
	node *lxsniffguard.Node
}

func (o *sniffGuardOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var conn net.Conn
	err := o.node.Dial(ctx, func(ctx context.Context) error {
		ctx, _ = adapter.ContextWithCarrierRecord(ctx)
		attemptConn, err := o.Outbound.DialContext(ctx, network, destination)
		if err != nil {
			if attemptConn != nil {
				attemptConn.Close()
			}
			return err
		}
		conn = attemptConn
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (o *sniffGuardOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	var conn net.PacketConn
	err := o.node.Dial(ctx, func(ctx context.Context) error {
		ctx, _ = adapter.ContextWithCarrierRecord(ctx)
		attemptConn, err := o.Outbound.ListenPacket(ctx, destination)
		if err != nil {
			if attemptConn != nil {
				attemptConn.Close()
			}
			return err
		}
		conn = attemptConn
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Forwarders of the optional interfaces, one per interface.

type sniffGuardStarter struct{ inner adapter.Lifecycle }

func (s sniffGuardStarter) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	return s.inner.Start(stage, scope)
}

type sniffGuardCloser struct{ inner io.Closer }

func (s sniffGuardCloser) Close() error { return s.inner.Close() }

type sniffGuardInterfaceListener struct {
	inner adapter.InterfaceUpdateListener
}

func (s sniffGuardInterfaceListener) InterfaceUpdated(ctx context.Context) {
	s.inner.InterfaceUpdated(ctx)
}

type sniffGuardIdleKeeper struct{ inner adapter.IdleConnectionKeeper }

func (s sniffGuardIdleKeeper) SetKeepIdleConnections(keep bool) { s.inner.SetKeepIdleConnections(keep) }
func (s sniffGuardIdleKeeper) CloseIdleConnections()            { s.inner.CloseIdleConnections() }

type sniffGuardMultiplex struct{ inner adapter.OutboundWithMultiplex }

func (s sniffGuardMultiplex) MultiplexEnabled() bool { return s.inner.MultiplexEnabled() }

// The variants: one per combination of optional interfaces that a covered
// node type has. A combination not listed here leaves the outbound unwrapped.

type sniffGuardOutboundStart struct {
	*sniffGuardOutbound
	sniffGuardStarter
}

type sniffGuardOutboundStartClose struct {
	*sniffGuardOutbound
	sniffGuardStarter
	sniffGuardCloser
}

type sniffGuardOutboundStartCloseListener struct {
	*sniffGuardOutbound
	sniffGuardStarter
	sniffGuardCloser
	sniffGuardInterfaceListener
}

type sniffGuardOutboundStartMultiplex struct {
	*sniffGuardOutbound
	sniffGuardStarter
	sniffGuardMultiplex
}

type sniffGuardOutboundStartListenerKeeperMultiplex struct {
	*sniffGuardOutbound
	sniffGuardStarter
	sniffGuardInterfaceListener
	sniffGuardIdleKeeper
	sniffGuardMultiplex
}

const (
	sniffGuardHasStart = 1 << iota
	sniffGuardHasClose
	sniffGuardHasListener
	sniffGuardHasKeeper
	sniffGuardHasMultiplex
)

// newSniffGuardOutbound returns the wrapper of inner, or nil when inner is a
// group or its method set has no exact variant.
func newSniffGuardOutbound(inner adapter.Outbound, node *lxsniffguard.Node) adapter.Outbound {
	if _, isGroup := inner.(adapter.OutboundGroup); isGroup {
		return nil
	}
	base := &sniffGuardOutbound{Outbound: inner, node: node}
	starter, hasStart := inner.(adapter.Lifecycle)
	closer, hasClose := inner.(io.Closer)
	listener, hasListener := inner.(adapter.InterfaceUpdateListener)
	keeper, hasKeeper := inner.(adapter.IdleConnectionKeeper)
	multiplex, hasMultiplex := inner.(adapter.OutboundWithMultiplex)
	var flags int
	if hasStart {
		flags |= sniffGuardHasStart
	}
	if hasClose {
		flags |= sniffGuardHasClose
	}
	if hasListener {
		flags |= sniffGuardHasListener
	}
	if hasKeeper {
		flags |= sniffGuardHasKeeper
	}
	if hasMultiplex {
		flags |= sniffGuardHasMultiplex
	}
	var wrapped adapter.Outbound
	switch flags {
	case 0:
		wrapped = base
	case sniffGuardHasStart:
		wrapped = &sniffGuardOutboundStart{base, sniffGuardStarter{starter}}
	case sniffGuardHasStart | sniffGuardHasClose:
		wrapped = &sniffGuardOutboundStartClose{base, sniffGuardStarter{starter}, sniffGuardCloser{closer}}
	case sniffGuardHasStart | sniffGuardHasClose | sniffGuardHasListener:
		wrapped = &sniffGuardOutboundStartCloseListener{base, sniffGuardStarter{starter}, sniffGuardCloser{closer}, sniffGuardInterfaceListener{listener}}
	case sniffGuardHasStart | sniffGuardHasMultiplex:
		wrapped = &sniffGuardOutboundStartMultiplex{base, sniffGuardStarter{starter}, sniffGuardMultiplex{multiplex}}
	case sniffGuardHasStart | sniffGuardHasListener | sniffGuardHasKeeper | sniffGuardHasMultiplex:
		wrapped = &sniffGuardOutboundStartListenerKeeperMultiplex{base, sniffGuardStarter{starter}, sniffGuardInterfaceListener{listener}, sniffGuardIdleKeeper{keeper}, sniffGuardMultiplex{multiplex}}
	default:
		return nil
	}
	if !sameMethodSet(reflect.TypeOf(inner), reflect.TypeOf(wrapped)) {
		// The inner outbound has a method the wrapper would hide (an
		// interface not forwarded here): wrapping would change what the core
		// sees of it.
		return nil
	}
	return wrapped
}

func sameMethodSet(a, b reflect.Type) bool {
	return reflect.DeepEqual(methodNames(a), methodNames(b))
}

func methodNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, t.Method(i).Name)
	}
	sort.Strings(names)
	return names
}
