//go:build with_lx_command

package wireguard

// lx: SPEC 139 — the endpoint's own stages of a connection test: `wake` from
// idle suspend and `wg_handshake` around a dial inside the tunnel.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func traceCtxLx(fresh bool) (context.Context, *lxtrace.Trace) {
	trace := lxtrace.New(lxtrace.Options{Fresh: fresh, Probe: true})
	return lxtrace.With(context.Background(), trace), trace
}

func stageOfLx(t *testing.T, summary lxtrace.Summary, kind string) lxtrace.StageSnapshot {
	t.Helper()
	for _, stage := range summary.Stages {
		if stage.Kind == kind {
			return stage
		}
	}
	t.Fatalf("no %s stage in %+v", kind, summary.Stages)
	return lxtrace.StageSnapshot{}
}

func attrLx(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func beginHandshakeTraceLx(ctx context.Context, tag string, statuses func() []adapter.PeerStatus) *handshakeTraceLx {
	_, stage := lxtrace.Begin(ctx, "wg_handshake", tag)
	return newHandshakeTraceLx(stage, statuses)
}

func asleepLx(t *testing.T, w *Endpoint, slept time.Duration) {
	t.Helper()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("precondition: must be asleep")
	}
	w.sleepSince.Store(time.Now().Add(-slept).UnixNano())
}

func TestConnTestWake_slept(t *testing.T) {
	w := newIdleTestEndpoint()
	asleepLx(t, w, 12*time.Minute)
	ctx, trace := traceCtxLx(false)
	if !w.resumeOnDial(ctx) {
		t.Fatal("the wake must succeed")
	}
	wake := stageOfLx(t, trace.Finish(nil), "wake")
	if wake.Result != lxtrace.OK || wake.Subject != "wg-test" {
		t.Fatalf("wake = %+v, want OK on wg-test", wake)
	}
	if attrLx(wake, "slept") != "720" || attrLx(wake, "mode") != "resume" {
		t.Fatalf("wake attrs = %v, want slept=720 mode=resume", wake.Attrs)
	}
}

func TestConnTestWake_failure(t *testing.T) {
	w := newIdleTestEndpoint()
	w.endpoint.SetResumeErrHookForTest(func() error { return E.New("bind: operation not permitted") })
	asleepLx(t, w, time.Minute)
	ctx, trace := traceCtxLx(false)
	if w.resumeOnDial(ctx) {
		t.Fatal("the wake must fail")
	}
	summary := trace.Finish(nil)
	wake := stageOfLx(t, summary, "wake")
	if wake.Result != lxtrace.Failed || summary.RootCause != wake.ID {
		t.Fatalf("wake = %+v root=%d, want FAILED root", wake, summary.RootCause)
	}
}

// An awake endpoint records nothing, and the L3-forward path (nil ctx) and a
// dial without a trace wake as before.
func TestConnTestWake_onlyWhenAsleep(t *testing.T) {
	w := newIdleTestEndpoint()
	ctx, trace := traceCtxLx(false)
	w.resumeOnDial(ctx)
	if stages := trace.Finish(nil).Stages; len(stages) != 0 {
		t.Fatalf("an awake endpoint must record nothing, got %+v", stages)
	}
	asleepLx(t, w, time.Minute)
	if !w.resumeOnDial(nil) {
		t.Fatal("the nil-ctx wake must succeed")
	}
	asleepLx(t, w, time.Minute)
	if !w.resumeOnDial(context.Background()) {
		t.Fatal("the wake without a trace must succeed")
	}
}

type fakeStatusesLx struct {
	before, after []adapter.PeerStatus
	calls         int
}

func (f *fakeStatusesLx) get() []adapter.PeerStatus {
	f.calls++
	if f.calls == 1 {
		return f.before
	}
	return f.after
}

func TestConnTestHandshake_new(t *testing.T) {
	ctx, trace := traceCtxLx(false)
	old := time.Now().Add(-10 * time.Minute)
	statuses := &fakeStatusesLx{before: []adapter.PeerStatus{{PublicKey: "pk", LastHandshake: old}}}
	begun := time.Now()
	handshake := beginHandshakeTraceLx(ctx, "awg-de", statuses.get)
	statuses.after = []adapter.PeerStatus{{PublicKey: "pk", LastHandshake: begun.Add(20 * time.Millisecond)}}
	time.Sleep(50 * time.Millisecond)
	handshake.end(nil)
	stage := stageOfLx(t, trace.Finish(nil), "wg_handshake")
	if stage.Result != lxtrace.OK || attrLx(stage, "handshake") != "new" || attrLx(stage, "peer") != "pk" {
		t.Fatalf("wg_handshake = %+v, want OK handshake=new peer=pk", stage)
	}
	// The stage ends at the handshake time, not when the dial returned.
	if stage.DurationMs < 15 || stage.DurationMs > 20 {
		t.Fatalf("duration = %d ms, want the handshake time (~20 ms)", stage.DurationMs)
	}
}

func TestConnTestHandshake_existing(t *testing.T) {
	for _, dialErr := range []error{nil, errors.New("connection refused")} {
		ctx, trace := traceCtxLx(true)
		last := []adapter.PeerStatus{{PublicKey: "pk", LastHandshake: time.Now().Add(-90 * time.Second)}}
		statuses := &fakeStatusesLx{before: last, after: last}
		beginHandshakeTraceLx(ctx, "wg", statuses.get).end(dialErr)
		stage := stageOfLx(t, trace.Finish(nil), "wg_handshake")
		if stage.Result != lxtrace.Reused || attrLx(stage, "handshake") != "existing" || attrLx(stage, "age") != "90" {
			t.Fatalf("dialErr=%v: wg_handshake = %+v, want REUSED existing age=90", dialErr, stage)
		}
	}
}

func TestConnTestHandshake_none(t *testing.T) {
	ctx, trace := traceCtxLx(false)
	expired := []adapter.PeerStatus{{PublicKey: "pk", LastHandshake: time.Now().Add(-time.Hour)}}
	statuses := &fakeStatusesLx{before: expired, after: expired}
	beginHandshakeTraceLx(ctx, "wg", statuses.get).end(errors.New("i/o timeout"))
	stage := stageOfLx(t, trace.Finish(nil), "wg_handshake")
	if stage.Result != lxtrace.Failed {
		t.Fatalf("wg_handshake = %+v, want FAILED without a handshake", stage)
	}

	ctx, trace = traceCtxLx(false)
	beginHandshakeTraceLx(ctx, "wg", (&fakeStatusesLx{}).get).end(nil)
	stage = stageOfLx(t, trace.Finish(nil), "wg_handshake")
	if stage.Result != lxtrace.NotEvaluated || attrLx(stage, "handshake") != "unknown" {
		t.Fatalf("wg_handshake = %+v, want NOT_EVALUATED unknown", stage)
	}
}

type failingDialerLx struct{ err error }

func (d failingDialerLx) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, d.err
}

func (d failingDialerLx) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, d.err
}

// A TCP dial inside a tunnel whose peer never answers: wg_handshake precedes
// tcp_connect and is the root cause; a UDP socket gets no handshake stage.
func TestConnTestHandshake_rootBeforeTCPConnect(t *testing.T) {
	w := newIdleTestEndpoint()
	ctx, trace := traceCtxLx(false)
	dialer := &netstackTraceDialerLx{Dialer: failingDialerLx{err: errors.New("i/o timeout")}, endpoint: w}
	destination := M.ParseSocksaddr("10.0.0.1:443")
	dialer.DialContext(ctx, N.NetworkTCP, destination)
	dialer.ListenPacket(ctx, destination)
	summary := trace.Finish(nil)
	if len(summary.Stages) != 3 {
		t.Fatalf("stages = %+v, want wg_handshake, tcp_connect, udp", summary.Stages)
	}
	handshake, connect := summary.Stages[0], summary.Stages[1]
	if handshake.Kind != "wg_handshake" || connect.Kind != "tcp_connect" || summary.Stages[2].Kind != "udp" {
		t.Fatalf("stages = %+v, want wg_handshake, tcp_connect, udp", summary.Stages)
	}
	if handshake.Result != lxtrace.Failed || summary.RootCause != handshake.ID {
		t.Fatalf("wg_handshake = %+v root=%d, want FAILED root", handshake, summary.RootCause)
	}
}

func TestConnTestHandshake_noTrace(t *testing.T) {
	w := newIdleTestEndpoint()
	if w.beginHandshakeLx(context.Background()) != nil {
		t.Fatal("no trace, no stage")
	}
	var handshake *handshakeTraceLx
	handshake.end(nil) // nil-safe
	if allocs := testing.AllocsPerRun(100, func() {
		w.beginHandshakeLx(context.Background()).end(nil)
		w.wakeLx(context.Background())()
	}); allocs != 0 {
		t.Fatalf("no-trace helpers allocate %v", allocs)
	}
}
