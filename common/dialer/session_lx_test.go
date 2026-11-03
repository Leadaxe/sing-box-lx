package dialer

import (
	"context"
	"net"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type ctxRecorder struct {
	dial   context.Context
	listen context.Context
}

func (r *ctxRecorder) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	r.dial = ctx
	return nil, net.ErrClosed
}

func (r *ctxRecorder) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	r.listen = ctx
	return nil, net.ErrClosed
}

var _ N.Dialer = (*ctxRecorder)(nil)

// lx: SPEC 152 — the underlay dial is internal to the tunnel: a selector in the
// detour interrupts it on every node switch, whatever
// `interrupt_exist_connections` says, and the first user's metadata does not
// leak into it.
func TestSessionDialerMarksUnderlayInternal(t *testing.T) {
	recorder := &ctxRecorder{}
	dialer := NewSessionDialerLx(recorder)
	userCtx := interrupt.ContextWithIsExternalConnection(context.Background(), true)
	userCtx = adapter.WithContext(userCtx, &adapter.InboundContext{Inbound: "tun-in", Outbound: "masque"})

	_, _ = dialer.DialContext(userCtx, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
	_, _ = dialer.ListenPacket(userCtx, M.Socksaddr{})
	for name, ctx := range map[string]context.Context{"dial": recorder.dial, "listen": recorder.listen} {
		if ctx == nil {
			t.Fatalf("%s: inner dialer not reached", name)
		}
		if interrupt.IsExternalConnectionFromContext(ctx) {
			t.Errorf("%s: underlay still marked external", name)
		}
		metadata := adapter.ContextFrom(ctx)
		if metadata == nil || metadata.Inbound != "" || metadata.Outbound != "masque" {
			t.Errorf("%s: session metadata = %+v, want fresh context keeping only Outbound", name, metadata)
		}
	}
	if dialer.(interface{ Upstream() any }).Upstream() != recorder {
		t.Error("Upstream() must expose the wrapped dialer")
	}
}
