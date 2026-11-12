package lxdatagram

import (
	"context"
	"net"
	"testing"

	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type ctxRecordingDialer struct {
	dial   context.Context
	listen context.Context
}

func (d *ctxRecordingDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.dial = ctx
	return nil, net.ErrClosed
}

func (d *ctxRecordingDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	d.listen = ctx
	return nil, net.ErrClosed
}

// lx: SPEC 152 — hysteria2 and tuic reach their underlay dialer only through
// Dialer; the session they open on it must be internal to the tunnel so a
// selector in the detour interrupts it on a node switch.
func TestDialerMarksSessionUnderlayInternal(t *testing.T) {
	inner := &ctxRecordingDialer{}
	dialer := Dialer(context.Background(), "hysteria2", inner, logger.NOP())
	userCtx := interrupt.ContextWithIsExternalConnection(context.Background(), true)
	_, _ = dialer.DialContext(userCtx, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
	_, _ = dialer.ListenPacket(userCtx, M.Socksaddr{})
	if inner.dial == nil || interrupt.IsExternalConnectionFromContext(inner.dial) {
		t.Error("DialContext: underlay still marked external")
	}
	if inner.listen == nil || interrupt.IsExternalConnectionFromContext(inner.listen) {
		t.Error("ListenPacket: underlay still marked external")
	}
}
