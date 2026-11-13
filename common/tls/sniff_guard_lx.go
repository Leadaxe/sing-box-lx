package tls

// lx: SPEC 060 — lx.sniff_guard on the TLS client handshake.
//
// The outbound wrapper puts an lxsniffguard.Attempt into the dial context of a
// covered node. ClientHandshake and the TLS dialer ask it, once per handshake,
// which form the ClientHello takes, and shape the raw conn before any engine
// sees it. Without an Attempt nothing changes: the node's own fragment and
// record_fragment work as upstream.
//
// The decision is per handshake, not per Config: a Config is shared by every
// dial of a node, and two concurrent handshakes may go different paths. So the
// shape travels on the conn. The conn handed to the engine is a *sniffGuardConn
// carrying the chosen writer; the std and uTLS clients (REALITY builds on the
// uTLS one) skip their own fragment wrapper when they see it, which is how an
// override replaces the node's keys, "no split" included.

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	tf "github.com/sagernet/sing-box/common/tlsfragment"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
)

// sniffGuardNode is what a client config asked of its own ClientHello.
type sniffGuardNode struct {
	fragment       bool
	recordFragment bool
	// chosen: fragment or record_fragment is set in the config; an explicit
	// `record_fragment: false` is a choice too (SPEC 156).
	chosen        bool
	fallbackDelay time.Duration
}

// sniffGuardEngine is implemented by the client configs whose ClientHello the
// guard can shape: the Go engines (std, uTLS, REALITY). The system engines
// (apple, windows) build the handshake out of reach and do not implement it.
type sniffGuardEngine interface {
	sniffGuardNodeLx() sniffGuardNode
}

func (c *STDClientConfig) sniffGuardNodeLx() sniffGuardNode {
	return sniffGuardNode{c.fragment, c.recordFragment, c.fragmentChosen, c.fragmentFallbackDelay}
}

// sniffGuardNodeOf returns the fragment keys of the engine under config, seen
// through the kTLS and ECH wrappers; ok is false for an engine that cannot be
// shaped.
func sniffGuardNodeOf(config Config) (node sniffGuardNode, ok bool) {
	for {
		switch wrapper := config.(type) {
		case *KTLSClientConfig:
			config = wrapper.Config
		case *ECHClientConfig:
			config = wrapper.ECHCapableConfig
		case sniffGuardEngine:
			return wrapper.sniffGuardNodeLx(), true
		default:
			return sniffGuardNode{}, false
		}
	}
}

// sniffGuardPath is the path of a dialled conn: a kernel socket is the direct
// path, otherwise the carrier recorded in the dial context; neither is an
// unknown path, which the guard leaves alone.
func sniffGuardPath(ctx context.Context, conn net.Conn) lxsniffguard.Path {
	if lxdatagram.IsKernelSocket(conn) {
		return lxsniffguard.Path{Direct: true}
	}
	if record := adapter.CarrierRecordFromContext(ctx); record != nil && record.Tag != "" {
		return lxsniffguard.Path{CarrierType: record.Type, CarrierTag: record.Tag}
	}
	return lxsniffguard.Path{}
}

// sniffGuardConnLx returns the conn the handshake writes its ClientHello to.
// It calls Attempt.Decide exactly once when the dial carries an Attempt and
// returns conn itself whenever the guard does not intervene.
func sniffGuardConnLx(ctx context.Context, conn net.Conn, config Config) net.Conn {
	attempt := lxsniffguard.AttemptFromContext(ctx)
	if attempt == nil {
		return conn
	}
	node, shapeable := sniffGuardNodeOf(config)
	applicable := shapeable && (!node.chosen || attempt.Override())
	decision := attempt.Decide(lxsniffguard.ProtoTLS, sniffGuardPath(ctx, conn), applicable)
	if !decision.Protect {
		if decision.Override && (node.fragment || node.recordFragment) {
			// The path is clean and the block overrides the node: no split,
			// whatever the node asked for.
			return &sniffGuardConn{Conn: conn}
		}
		return conn
	}
	switch decision.Method {
	case option.LXSniffGuardTLSRecordFragment:
		return &sniffGuardConn{Conn: tf.NewConn(conn, ctx, false, true, node.fallbackDelay)}
	case option.LXSniffGuardTLSFragment:
		return &sniffGuardConn{Conn: tf.NewConn(conn, ctx, true, false, node.fallbackDelay)}
	case option.LXSniffGuardDelay:
		return &sniffGuardConn{Conn: &sniffGuardDelayConn{Conn: conn, ctx: ctx, delay: decision.Delay}}
	default:
		return conn
	}
}

// sniffGuardShapedLx reports whether the guard decided the form of the
// ClientHello written to conn: the engine then leaves the node's fragment
// keys out.
func sniffGuardShapedLx(conn net.Conn) bool {
	_, shaped := conn.(*sniffGuardConn)
	return shaped
}

// sniffGuardConn marks a conn whose ClientHello form the guard decided; the
// embedded conn writes it.
type sniffGuardConn struct {
	net.Conn
}

func (c *sniffGuardConn) ReaderReplaceable() bool {
	return true
}

func (c *sniffGuardConn) WriterReplaceable() bool {
	if writer, isWriter := c.Conn.(N.WriterWithUpstream); isWriter {
		return writer.WriterReplaceable()
	}
	return true
}

func (c *sniffGuardConn) Upstream() any {
	return c.Conn
}

// sniffGuardDelayConn is the `delay` method: the ClientHello stays one whole
// TLS record but goes out in two writes, cut inside the SNI host name, with a
// pause between them. A sniffer that reads the first chunk sees an incomplete
// record and gives up once its wait budget runs out before the second one.
type sniffGuardDelayConn struct {
	net.Conn
	ctx     context.Context
	delay   time.Duration
	written bool
}

func (c *sniffGuardDelayConn) Write(b []byte) (int, error) {
	if c.written || len(b) == 0 {
		return c.Conn.Write(b)
	}
	c.written = true
	split := sniffGuardDelaySplit(b)
	if split <= 0 {
		return c.Conn.Write(b)
	}
	n, err := c.Conn.Write(b[:split])
	if err != nil {
		return n, err
	}
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-c.ctx.Done():
		return n, c.ctx.Err()
	}
	m, err := c.Conn.Write(b[split:])
	return n + m, err
}

// sniffGuardDelaySplit returns where the first chunk of a ClientHello record
// ends: in the middle of the SNI host name. 0 when b holds no complete
// ClientHello record with a host name.
func sniffGuardDelaySplit(b []byte) int {
	serverName := tf.IndexTLSServerName(b)
	if serverName == nil || serverName.Length == 0 {
		return 0
	}
	return serverName.Index + serverName.Length/2
}

func (c *sniffGuardDelayConn) ReaderReplaceable() bool {
	return true
}

func (c *sniffGuardDelayConn) WriterReplaceable() bool {
	return c.written
}

func (c *sniffGuardDelayConn) Upstream() any {
	return c.Conn
}
