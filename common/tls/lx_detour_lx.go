package tls

// lx: SPEC 165 — the lx.detour.strip filters on the TLS client handshake
// (ALGORITHM С1–С5).
//
// A node with `detour` (after the chain rewrite) whose dialled conn is not a
// kernel socket is dialled through a tunnel or a proxy stream. On such a conn
// the filters keep the node from mechanisms that need the kernel socket:
//   - tls.fragment drops the node's own ClientHello split, which can not wait
//     for the kernel to acknowledge each piece and sleeps
//     fragment_fallback_delay between them; record_fragment stays;
//   - tls.spoof skips the forged ClientHello, tls.kernel skips kTLS;
//   - tls.engine hands an apple-engine handshake to the go engine.
// The decision is per handshake, made at the seam where lx.sniff_guard
// shapes the ClientHello, and travels on the conn: the spoof and kTLS steps
// look for the mark under their conn.

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	aTLS "github.com/sagernet/sing/common/tls"
	"github.com/sagernet/sing/service"
)

// lxDetourDecision is what lx.detour does to one handshake.
type lxDetourDecision struct {
	tag           string
	stripFragment bool
	stripSpoof    bool
	stripKernel   bool
	stripEngine   bool
	// recordFragmentSet: the node sets record_fragment explicitly (SPEC 156).
	recordFragmentSet bool
	resolved          *option.LXResolved
	// ctx is the build context of the node's TLS config; handshake the
	// context of the handshake, for its trace.
	ctx       context.Context
	handshake context.Context
}

// lxDetourDecide decides for the handshake of node over conn. A node outside
// lx.detour (no detour, excepted, both rules off) or a kernel socket gets the
// zero decision.
func lxDetourDecide(node sniffGuardNode, conn net.Conn) lxDetourDecision {
	decision := lxDetourDecideContext(node.ctx, conn)
	decision.stripFragment = decision.stripFragment && node.fragment
	return decision
}

// lxDetourDecideContext decides from the build context of the node's TLS
// config: its tag and the box's lx values.
func lxDetourDecideContext(ctx context.Context, conn net.Conn) lxDetourDecision {
	if ctx == nil {
		return lxDetourDecision{}
	}
	resolved := service.PtrFromContext[option.LXResolved](ctx)
	if resolved == nil || len(resolved.DetourNodes) == 0 {
		return lxDetourDecision{}
	}
	tag := adapter.OutboundTagFromContext(ctx)
	if !resolved.DetourNodes[tag] || lxdatagram.IsKernelSocket(conn) {
		return lxDetourDecision{}
	}
	strip := resolved.Detour.Strip
	return lxDetourDecision{
		tag:           tag,
		stripFragment: strip.TLSFragment,
		stripSpoof:    strip.TLSSpoof,
		stripKernel:   strip.TLSKernel,
		stripEngine:   strip.TLSEngine,
		resolved:      resolved,
		ctx:           ctx,

		recordFragmentSet: resolved.DetourRecordFragmentSet[tag],
	}
}

// strippedNode is the node as the guard sees it once the split is dropped:
// a dropped split is not the node's choice (С3).
func (d lxDetourDecision) strippedNode(node sniffGuardNode) sniffGuardNode {
	if !d.stripFragment {
		return node
	}
	node.fragment = false
	node.chosen = d.recordFragmentSet
	return node
}

// applied records a filter that changed this handshake: in the connection
// observability and once per node in the log (С4).
func (d *lxDetourDecision) applied(ctx context.Context, filter string) {
	d.logOnce(filter)
	if ctx == nil || lxtrace.From(ctx) == nil {
		return
	}
	_, stage := lxtrace.Begin(ctx, "detour_strip", d.tag)
	if stage == nil {
		return
	}
	stage.Attr("filter", filter)
	stage.End(nil)
}

// lxKernelOnlyConn marks a conn whose handshake must skip tls.spoof or kTLS.
type lxKernelOnlyConn struct {
	net.Conn
	decision lxDetourDecision
}

func (c *lxKernelOnlyConn) ReaderReplaceable() bool { return true }
func (c *lxKernelOnlyConn) WriterReplaceable() bool { return true }
func (c *lxKernelOnlyConn) Upstream() any           { return c.Conn }

// mark puts the skip mark on conn when the decision asks for
// it; a shaped conn carries it in its own field so the engine still sees the
// sniffGuardConn on top.
func (d lxDetourDecision) mark(conn net.Conn) net.Conn {
	if !d.stripSpoof && !d.stripKernel {
		return conn
	}
	if shaped, isShaped := conn.(*sniffGuardConn); isShaped {
		shaped.kernelOnly = &d
		return shaped
	}
	return &lxKernelOnlyConn{Conn: conn, decision: d}
}

// kernelOnlyMarkLx returns the skip decision found under conn, or nil.
func kernelOnlyMarkLx(conn net.Conn) *lxDetourDecision {
	var current any = conn
	for current != nil {
		switch marked := current.(type) {
		case *lxKernelOnlyConn:
			return &marked.decision
		case *sniffGuardConn:
			if marked.kernelOnly != nil {
				return marked.kernelOnly
			}
		}
		upstream, hasUpstream := current.(interface{ Upstream() any })
		if !hasUpstream {
			return nil
		}
		current = upstream.Upstream()
	}
	return nil
}

// skipSpoofLx is the tls.spoof filter: true when the spoof must not be
// applied to conn.
func skipSpoofLx(conn net.Conn, spoof string) bool {
	if spoof == "" {
		return false
	}
	decision := kernelOnlyMarkLx(conn)
	if decision == nil || !decision.stripSpoof {
		return false
	}
	decision.applied(decision.handshake, option.LXDetourStripTLSSpoof)
	return true
}

// skipKTLSLx is the tls.kernel filter: true when kTLS must not be turned on
// over conn.
func skipKTLSLx(conn net.Conn) bool {
	decision := kernelOnlyMarkLx(conn)
	if decision == nil || !decision.stripKernel {
		return false
	}
	decision.applied(decision.handshake, option.LXDetourStripTLSKernel)
	return true
}

// logOnce writes one line per node, box and filter; the box keeps the set.
func (d *lxDetourDecision) logOnce(filter string) {
	if d.resolved.DetourLogged != nil {
		if _, loaded := d.resolved.DetourLogged.LoadOrStore(d.tag+"\x00"+filter, struct{}{}); loaded {
			return
		}
	}
	factory := service.FromContext[log.Factory](d.ctx)
	if factory == nil {
		return
	}
	factory.NewLogger("lx.detour").Info(d.tag, ": strip ", filter, ": no kernel socket under detour")
}

// lxAppleConfig is an apple-engine config with the go engine at hand for a
// handshake over a conn without a kernel socket (the tls.engine filter). The
// fingerprint of such a handshake is Go's.
type lxAppleConfig struct {
	Config
	fallback Config
	ctx      context.Context
}

// newAppleClientLx builds the apple client and, beside it, the go client of
// the same options. Without a go client (the options do not build one) the
// filter cannot act and the apple config is returned as upstream does.
func newAppleClientLx(options ClientOptions) (Config, error) {
	apple, err := newAppleClient(options.Context, options.Logger, options.ServerAddress, options.Options, options.AllowEmptyServerName)
	if err != nil {
		return nil, err
	}
	goOptions := options
	goOptions.Options.Engine = C.TLSEngineGo
	fallback, err := NewClientWithOptions(goOptions)
	if err != nil {
		return apple, nil
	}
	return &lxAppleConfig{Config: apple, fallback: fallback, ctx: options.Context}, nil
}

func (c *lxAppleConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	decision := lxDetourDecideContext(c.ctx, conn)
	if decision.stripEngine {
		decision.applied(ctx, option.LXDetourStripTLSEngine)
		// The guard decided on this handshake at the outer seam already, for
		// an engine it cannot shape; it is asked once per handshake, so the
		// go engine only gets the lx.detour filters.
		guardless := lxsniffguard.WithoutAttempt(ctx)
		return aTLS.ClientHandshake(guardless, sniffGuardConnLx(guardless, conn, c.fallback), c.fallback)
	}
	return aTLS.ClientHandshake(ctx, conn, c.Config)
}

func (c *lxAppleConfig) Clone() Config {
	return &lxAppleConfig{Config: c.Config.Clone(), fallback: c.fallback.Clone(), ctx: c.ctx}
}
