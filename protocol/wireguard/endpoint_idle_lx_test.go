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
	w.SuspendIfIdle(true, 30*time.Second, 0)
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
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("a freshly-active endpoint must not be suspended")
	}
}

func TestSuspendIfIdle_suspendsWhenIdleAndUnreachable(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
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
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("just under threshold must not suspend")
	}
	w.lastActivity.Store(time.Now().Add(-31 * time.Second).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("past threshold must suspend")
	}
}

func TestSuspendIfIdle_idempotentCAS(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("first call must suspend")
	}
	// Second call is a no-op (already asleep) — must not panic, state unchanged.
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() || w.started.Load() {
		t.Fatal("double-suspend must leave state asleep/not-started")
	}
}

// TestSuspendIfIdle_guardSuspendedNotTouched is the §8 invariant verified live on
// an AWG-over-WG endpoint (wg-3 in the prod run): a guard-suspended endpoint has
// started=false WITHOUT idleAsleep. The idle tick must early-return on !started and
// NOT flip idleAsleep — otherwise a later resumeOnDial would idle-wake it and
// re-trigger the AWG-over-WG kernel hang the guard exists to prevent.
func TestSuspendIfIdle_guardSuspendedNotTouched(t *testing.T) {
	w := newIdleTestEndpoint()
	w.started.Store(false) // guard-suspend (device.Down at Start), idleAsleep stays false
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("a guard-suspended endpoint must NOT be flagged idleAsleep by the tick")
	}
	if w.started.Load() {
		t.Fatal("the tick must not change started for a guard-suspended endpoint")
	}
}

func TestResumeOnDial_wakesAndStamps(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0) // now asleep
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
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("a dial right before the tick must keep the endpoint awake (fresh stamp)")
	}
}

// TestSuspendIfIdle_reachableThreshold pins the lx_idle_suspend_reachable
// semantics: reachable + long-idle suspends ONLY when the second threshold is
// set and exceeded.
func TestSuspendIfIdle_reachableThreshold(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-10 * time.Minute).UnixNano())
	// Reachable, threshold disabled (0): never suspends however idle.
	w.SuspendIfIdle(true, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("reachable endpoint must not suspend with reachableThreshold=0")
	}
	// Reachable, idle < reachableThreshold: stays up.
	w.SuspendIfIdle(true, 30*time.Second, time.Hour)
	if w.idleAsleep.Load() {
		t.Fatal("reachable endpoint under the reachable threshold must stay up")
	}
	// Reachable, idle > reachableThreshold: suspends.
	w.SuspendIfIdle(true, 30*time.Second, 5*time.Minute)
	if !w.idleAsleep.Load() {
		t.Fatal("reachable endpoint past lx_idle_suspend_reachable must suspend")
	}
	if w.started.Load() {
		t.Fatal("started must be false after reachable-idle suspend")
	}
	// And a dial wakes it like any idle-suspended endpoint.
	if !w.resumeOnDial() {
		t.Fatal("dial must wake a reachable-idle-suspended endpoint")
	}
}

// TestSuspendIfIdle_listenModeNeverSuspends: a listen_port endpoint has no dial
// path to wake it — the tick must never touch it.
func TestSuspendIfIdle_listenModeNeverSuspends(t *testing.T) {
	w := newIdleTestEndpoint()
	w.listenMode = true
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() || !w.started.Load() {
		t.Fatal("a listen-mode endpoint must never be idle-suspended")
	}
}

// TestSuspendIfIdle_transferDeltaGate: a byte delta >= the threshold since the
// last decision (real non-TCP traffic on established flows) must refresh the
// activity clock instead of suspending mid-transfer; a sub-threshold delta
// (keepalive/rekey noise — or silence) must NOT block the suspend, or a
// persistent_keepalive peer would never sleep.
func TestSuspendIfIdle_transferDeltaGate(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	// The nil-device transport reports TransferTotals()==0; seed a non-zero
	// last-seen sum so the uint64 delta (0 - 12345 wraps huge) clears the
	// threshold — the "real traffic volume" branch.
	w.lastTransferSum = 12345
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if w.idleAsleep.Load() {
		t.Fatal("an above-threshold transfer delta must veto the suspend")
	}
	if w.IdleSince() > time.Second {
		t.Fatal("an above-threshold transfer delta must refresh the activity clock")
	}
	if w.lastTransferSum != 0 {
		t.Fatal("the observed sum must be recorded for the next decision")
	}
	// Delta below the threshold now (0 → 0, i.e. keepalive-scale noise) and idle
	// again → suspends as before.
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("sub-threshold counter noise must not keep the endpoint awake")
	}
}

// --- SPEC 020 level 3: lx_idle_teardown -------------------------------------

// TestTeardownIfSlept_onlyAfterSleepWindow: the teardown clock counts from the
// moment of falling asleep, not from the last dial — an endpoint that just went
// to sleep is never torn down in the same tick, and only crosses the line once
// it has SLEPT past the window.
func TestTeardownIfSlept_onlyAfterSleepWindow(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	if !w.idleAsleep.Load() {
		t.Fatal("precondition: must be asleep")
	}
	// Just fell asleep (slept ~0) → teardown must not fire, however idle it was.
	w.TeardownIfSlept(5 * time.Minute)
	if w.torndown.Load() {
		t.Fatal("an endpoint that just fell asleep must not be torn down")
	}
	// Backdate the sleep clock past the window → tears down.
	w.sleepSince.Store(time.Now().Add(-6 * time.Minute).UnixNano())
	w.TeardownIfSlept(5 * time.Minute)
	if !w.torndown.Load() {
		t.Fatal("an endpoint asleep past lx_idle_teardown must be torn down")
	}
	if !w.idleAsleep.Load() {
		t.Fatal("a torn-down endpoint is still idle-asleep (it is a deeper sleep, not a new state)")
	}
}

// TestTeardownIfSlept_disabledAndAwake: 0 = off; an awake endpoint is never torn
// down regardless of its dial clock.
func TestTeardownIfSlept_disabledAndAwake(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(0)
	if w.torndown.Load() {
		t.Fatal("threshold 0 disables teardown")
	}

	awake := newIdleTestEndpoint()
	awake.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano()) // stale clock, but not asleep
	awake.TeardownIfSlept(time.Minute)
	if awake.torndown.Load() {
		t.Fatal("an awake endpoint must never be torn down")
	}
}

// TestTeardownIfSlept_guardSuspendedNotTornDown pins the SPEC 007 invariant at
// level 3: a guard-suspended endpoint has idleAsleep=false, so the teardown pass
// must skip it (a torn-down guard endpoint could be rebuilt by a dial).
func TestTeardownIfSlept_guardSuspendedNotTornDown(t *testing.T) {
	w := newIdleTestEndpoint()
	w.started.Store(false) // guard-suspend: started=false WITHOUT idleAsleep
	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(time.Minute)
	if w.torndown.Load() {
		t.Fatal("a guard-suspended endpoint must not be torn down by the idle tick")
	}
}

// TestSuspendAmneziaWG_clearsTeardownState: if the guard takes over an already
// torn-down endpoint, it must clear torndown too — otherwise resumeOnDial's
// rebuild branch would resurrect exactly what the guard is holding down.
func TestSuspendAmneziaWG_clearsTeardownState(t *testing.T) {
	w := newIdleTestEndpoint()
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, 30*time.Second, 0)
	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(time.Minute)
	if !w.torndown.Load() {
		t.Fatal("precondition: must be torn down")
	}
	w.SuspendAmneziaWG()
	if w.torndown.Load() || w.idleAsleep.Load() {
		t.Fatal("the guard must clear both idle flags so no dial can rebuild the endpoint")
	}
	if ok := w.resumeOnDial(); ok {
		t.Fatal("a dial must not resurrect a guard-owned endpoint")
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
