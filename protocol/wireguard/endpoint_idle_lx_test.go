// lx:begin idle-suspend
package wireguard

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter/endpoint"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
)

// newIdleTestEndpoint builds an Endpoint with just enough wired up to exercise the
// SPEC 020 idle-suspend logic: a nil-device transport endpoint (Suspend/Resume are
// nil-safe no-ops), a NOP logger, and started=true. No real WireGuard device.
func newIdleTestEndpoint() *Endpoint {
	w := &Endpoint{
		Adapter:  endpoint.NewAdapter(C.TypeWireGuard, "wg-test", []string{N.NetworkTCP}, nil),
		logger:   logger.NOP(),
		endpoint: &wireguard.Endpoint{}, // nil device → Suspend/Resume no-op
	}
	w.started.Store(true)
	return w
}

func TestIdleSinceNeverDialed(t *testing.T) {
	w := &Endpoint{} // lastActivity == 0
	if got := w.IdleSince(); got < time.Hour*24*365 {
		t.Fatalf("a never-stamped endpoint should report a huge idle duration, got %v", got)
	}
}

func TestIdleSinceAfterStamp(t *testing.T) {
	w := newIdleTestEndpoint()
	w.stampActivity()
	if got := w.IdleSince(); got > time.Second {
		t.Fatalf("idle right after stamp should be ~0, got %v", got)
	}
}

func TestSuspendIfIdle_reachableNeverSuspends(t *testing.T) {
	w := newIdleTestEndpoint()
	// Make it look very idle, but reachable=true must veto suspension.
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(true, 30*time.Second)
	if w.idleAsleep.Load() {
		t.Fatal("a reachable endpoint must not be suspended")
	}
	if !w.started.Load() {
		t.Fatal("started must stay true for a reachable endpoint")
	}
}

func TestSuspendIfIdle_notIdleEnough(t *testing.T) {
	w := newIdleTestEndpoint()
	w.stampActivity() // just dialed → not idle
	w.SuspendIfIdle(false, 30*time.Second)
	if w.idleAsleep.Load() {
		t.Fatal("a freshly-active endpoint must not be suspended")
	}
}

func TestSuspendIfIdle_suspendsWhenIdleAndUnreachable(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second)
	if !w.idleAsleep.Load() {
		t.Fatal("an idle + unreachable endpoint must be suspended")
	}
	if w.started.Load() {
		t.Fatal("started must be false after idle-suspend")
	}
}

func TestSuspendIfIdle_thresholdBoundary(t *testing.T) {
	w := newIdleTestEndpoint()
	// Exactly at threshold is NOT past it (IdleSince() < threshold is false only when strictly greater).
	w.lastActivity.Store(time.Now().Add(-29 * time.Second).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second)
	if w.idleAsleep.Load() {
		t.Fatal("just under threshold must not suspend")
	}
	w.lastActivity.Store(time.Now().Add(-31 * time.Second).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second)
	if !w.idleAsleep.Load() {
		t.Fatal("past threshold must suspend")
	}
}

func TestSuspendIfIdle_idempotentCAS(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second)
	if !w.idleAsleep.Load() {
		t.Fatal("first call must suspend")
	}
	// Second call is a no-op (already asleep) — must not panic, state unchanged.
	w.SuspendIfIdle(false, 30*time.Second)
	if !w.idleAsleep.Load() || w.started.Load() {
		t.Fatal("double-suspend must leave state asleep/not-started")
	}
}

func TestResumeOnDial_wakesAndStamps(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second) // now asleep
	if !w.idleAsleep.Load() {
		t.Fatal("precondition: must be asleep")
	}
	ok := w.resumeOnDial()
	if !ok {
		t.Fatal("resumeOnDial on an idle-asleep endpoint must wake and return true")
	}
	if w.idleAsleep.Load() || !w.started.Load() {
		t.Fatal("after wake: idleAsleep=false, started=true")
	}
	if w.IdleSince() > time.Second {
		t.Fatal("resumeOnDial must stamp activity (idle ~0 after)")
	}
}

func TestResumeOnDial_dialBeforeTickRace(t *testing.T) {
	// A dial immediately before the tick stamps activity first, so the subsequent
	// SuspendIfIdle sees IdleSince() < threshold and does NOT suspend.
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.resumeOnDial() // stamps now; endpoint was awake, so just a stamp
	w.SuspendIfIdle(false, 30*time.Second)
	if w.idleAsleep.Load() {
		t.Fatal("a dial right before the tick must keep the endpoint awake (fresh stamp)")
	}
}

func TestResumeOnDial_guardSuspendedNotWoken(t *testing.T) {
	// A guard-suspended endpoint has started=false but idleAsleep=false.
	// resumeOnDial must NOT wake it (returns started, i.e. false).
	w := newIdleTestEndpoint()
	w.started.Store(false) // simulate guard/awg-chain suspend (not idle)
	ok := w.resumeOnDial()
	if ok {
		t.Fatal("resumeOnDial must not resurrect a guard-suspended (non-idle) endpoint")
	}
	if w.idleAsleep.Load() {
		t.Fatal("guard-suspended endpoint must not be flagged idleAsleep")
	}
}

// lx:end idle-suspend
