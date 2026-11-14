package trusttunnel

// lx: SPEC 060 — lx.sniff_guard on the HTTP/3 session of a trusttunnel
// outbound, through the transport's QUICDialHook: each attempt to open the
// session runs inside the node's Node.Dial with a carrier record of its own,
// and its QUIC handshake gets the guard's form.

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/transport/trusttunnel"
	"github.com/sagernet/sing/common/logger"
)

type sniffGuardHook struct {
	node   *lxsniffguard.Node
	logger logger.Logger
}

// newSniffGuardHook returns the hook for node, or nil when lx.sniff_guard
// does not cover it.
func newSniffGuardHook(node *lxsniffguard.Node, logger logger.Logger) trusttunnel.QUICDialHook {
	if node == nil {
		return nil
	}
	return sniffGuardHook{node: node, logger: logger}
}

func (h sniffGuardHook) Dial(ctx context.Context, open func(ctx context.Context) error) error {
	return h.node.Dial(ctx, func(ctx context.Context) error {
		ctx, _ = adapter.ContextWithCarrierRecord(ctx)
		return open(ctx)
	})
}

func (h sniffGuardHook) Handshake(ctx context.Context, conn net.Conn) (net.Conn, func()) {
	handshakeConn, handshake := lxdatagram.GuardQUIC(lxsniffguard.AttemptFromContext(ctx), conn, adapter.CarrierRecordFromContext(ctx), conn, h.logger)
	return handshakeConn, handshake.Done
}
