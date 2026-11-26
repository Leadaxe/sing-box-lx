package masque

// lx: pump-death visibility — a tunnel that dies on its own must leave exactly
// one Warn naming the failing operation, and a deliberate teardown
// (idle-suspend, Close) must stay silent. Without this the only trace of a
// dead tunnel was the next "establishing" line (LxBox chain debugging,
// 2026-08-24).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type failingIpConn struct {
	err    error
	closed atomic.Bool
}

func (c *failingIpConn) ReadPacket() ([]byte, error)        { return nil, c.err }
func (c *failingIpConn) WritePacket([]byte) ([]byte, error) { return nil, c.err }
func (c *failingIpConn) Close() error                       { c.closed.Store(true); return nil }

func TestPumpFatalLogsOnLiveCtxOnly(t *testing.T) {
	t.Parallel()
	rec := &warnRecorder{}
	o := &Outbound{logger: rec}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{ctx: ctx, cancel: cancel}

	o.pumpFatal(s, "read from tunnel", errors.New("boom"))
	if rec.count() != 1 {
		t.Fatalf("live ctx must produce exactly one warn, got %v", rec.warns)
	}

	cancel()
	o.pumpFatal(s, "read from tunnel", errors.New("boom"))
	if rec.count() != 1 {
		t.Fatalf("cancelled ctx (deliberate teardown) must stay silent, got %v", rec.warns)
	}
}

// End-to-end through a real pump: the tunnel read fails → one Warn naming the
// operation and the cause, then the session is torn down (ipConn closed, ctx
// cancelled) so the paired pump wakes under a cancelled ctx and stays silent.
func TestPumpFromTunnelWarnsOnceAndTearsDown(t *testing.T) {
	t.Parallel()
	rec := &warnRecorder{}
	o := &Outbound{logger: rec}
	ctx, cancel := context.WithCancel(context.Background())
	conn := &failingIpConn{err: errors.New("quic gone")}
	s := &session{ctx: ctx, cancel: cancel, ipConn: conn}
	o.sess = s

	o.pumpFromTunnel(s) // runs synchronously: read error → warn → teardown

	if rec.count() != 1 {
		t.Fatalf("want exactly one warn, got %v", rec.warns)
	}
	if !strings.Contains(rec.warns[0], "read from tunnel") || !strings.Contains(rec.warns[0], "quic gone") {
		t.Fatalf("warn must name the operation and the cause, got %q", rec.warns[0])
	}
	if s.ctx.Err() == nil {
		t.Fatal("pump death must tear the session down")
	}
	if !conn.closed.Load() {
		t.Fatal("teardown must close the tunnel conn")
	}
	o.runMu.Lock()
	cleared := o.sess == nil
	o.runMu.Unlock()
	if !cleared {
		t.Fatal("teardown must clear the current session so the next dial rebuilds")
	}
}
