//go:build with_lx_command

package tls

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 §5.13 — a handshake cut short by a cancelled ctx lost a race
// a sibling won (a DNS group's parallel query answered by another member):
// SKIPPED with lost_race, not FAILED. A handshake that ran out of time stays
// FAILED (§5.11).

func lxStalledHandshake(t *testing.T, makeCtx func(context.Context) (context.Context, context.CancelFunc)) lxtrace.StageSnapshot {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Accepts and never answers the ClientHello.
			defer conn.Close()
		}
	}()
	config, err := NewSTDClient(context.Background(), logger.NOP(), "example.com", option.OutboundTLSOptions{Enabled: true, ServerName: "example.com"})
	require.NoError(t, err)
	tcpConn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer tcpConn.Close()
	trace := lxtrace.New(lxtrace.Options{})
	ctx, cancel := makeCtx(lxtrace.With(context.Background(), trace))
	defer cancel()
	_, err = ClientHandshake(ctx, tcpConn, config)
	require.Error(t, err)
	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, "tls", summary.Stages[0].Kind)
	return summary.Stages[0]
}

func lxAttr(stage lxtrace.StageSnapshot, key string) (string, bool) {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return "", false
}

func TestLxHandshakeCancelledLostRace(t *testing.T) {
	stage := lxStalledHandshake(t, func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(50*time.Millisecond, cancel)
		return ctx, cancel
	})
	require.Equal(t, lxtrace.Skipped, stage.Result)
	_, lostRace := lxAttr(stage, "lost_race")
	require.True(t, lostRace, "attrs = %v", stage.Attrs)
}

func TestLxHandshakeTimeoutStaysFailed(t *testing.T) {
	stage := lxStalledHandshake(t, func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 50*time.Millisecond)
	})
	require.Equal(t, lxtrace.Failed, stage.Result)
	_, lostRace := lxAttr(stage, "lost_race")
	require.False(t, lostRace, "attrs = %v", stage.Attrs)
}
