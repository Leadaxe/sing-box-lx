package wireguard

// lx: SPEC 106 — manual enable/disable of the endpoint. The nil-device harness
// (newIdleTestEndpoint) covers the flags; the lazy harness with fake devices
// covers the rebuild and the budget.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func dialErr(w *Endpoint) error {
	_, err := w.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("10.0.0.9:80"))
	return err
}

func assertDisabledRefusal(t *testing.T, w *Endpoint) {
	t.Helper()
	if err := dialErr(w); !errors.Is(err, errEndpointDisabled) {
		t.Fatalf("dial on a disabled endpoint: got %v, want %v", err, errEndpointDisabled)
	}
	if _, err := w.ListenPacket(context.Background(), M.ParseSocksaddr("10.0.0.9:53")); !errors.Is(err, errEndpointDisabled) {
		t.Fatalf("listen on a disabled endpoint: got %v", err)
	}
	if err := w.WritePackets(nil); !errors.Is(err, errEndpointDisabled) {
		t.Fatalf("L3 forward on a disabled endpoint: got %v", err)
	}
}

func TestSetEnabled_disableAwake(t *testing.T) {
	w := newIdleTestEndpoint()
	if !w.Enabled() {
		t.Fatal("a new endpoint is enabled")
	}
	if err := w.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if w.Enabled() || w.started.Load() || !w.idleAsleep.Load() || w.sleepSince.Load() == 0 {
		t.Fatal("disable of an awake endpoint: !started, idleAsleep, sleep clock stamped")
	}
	if !w.endpoint.Suspended() {
		t.Fatal("disable must bring the device down")
	}
	assertState(t, w, adapter.EndpointStateDisabled)
	assertDisabledRefusal(t, w)
	if w.idleAsleep.Load() != true || w.started.Load() {
		t.Fatal("a refused dial must not wake the endpoint")
	}
	// The idle tick leaves it alone.
	w.SuspendIfIdle(false, 0, 0)
	assertState(t, w, adapter.EndpointStateDisabled)
}

func TestSetEnabled_disableAsleepAndTornDown(t *testing.T) {
	asleep := newIdleTestEndpoint()
	asleep.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	asleep.SuspendIfIdle(false, time.Minute, 0)
	sleepSince := asleep.sleepSince.Load()
	if err := asleep.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if asleep.sleepSince.Load() != sleepSince || asleep.started.Load() || !asleep.idleAsleep.Load() {
		t.Fatal("disable of a sleeper only sets the flag")
	}
	assertState(t, asleep, adapter.EndpointStateDisabled)
	assertDisabledRefusal(t, asleep)

	torn := newIdleTestEndpoint()
	torn.started.Store(false)
	torn.idleAsleep.Store(true)
	torn.torndown.Store(true)
	if err := torn.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if !torn.torndown.Load() || torn.started.Load() {
		t.Fatal("disable of a torn-down endpoint only sets the flag")
	}
	assertState(t, torn, adapter.EndpointStateDisabled)
	assertDisabledRefusal(t, torn)
}

func TestSetEnabled_enableWakes(t *testing.T) {
	w := newIdleTestEndpoint()
	var resumes int
	w.endpoint.SetResumeErrHookForTest(func() error {
		resumes++
		return nil
	})
	_ = w.SetEnabled(false)
	if err := w.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if resumes != 1 {
		t.Fatalf("enable must wake the device once, got %d", resumes)
	}
	if !w.Enabled() || !w.started.Load() || w.idleAsleep.Load() || w.sleepSince.Load() != 0 {
		t.Fatal("after enable: started, not asleep, sleep clock cleared")
	}
	assertState(t, w, adapter.EndpointStateUp)
	if !w.resumeOnDial(context.Background()) {
		t.Fatal("an enabled endpoint is dialable")
	}
}

func TestSetEnabled_enableWakeFailureKeepsAsleep(t *testing.T) {
	w := newIdleTestEndpoint()
	_ = w.SetEnabled(false)
	w.endpoint.SetResumeErrHookForTest(func() error {
		return E.New("bind: operation not permitted")
	})
	if err := w.SetEnabled(true); err == nil {
		t.Fatal("a failed wake must be returned")
	}
	if !w.Enabled() {
		t.Fatal("the switch is on even when the wake failed")
	}
	if w.started.Load() || !w.idleAsleep.Load() {
		t.Fatal("a failed wake leaves the endpoint asleep")
	}
	assertState(t, w, adapter.EndpointStateAsleep)
	// The next dial retries the wake.
	w.endpoint.SetResumeErrHookForTest(nil)
	if !w.resumeOnDial(context.Background()) || !w.started.Load() {
		t.Fatal("the next dial must wake the endpoint")
	}
}

func TestSetEnabled_idempotent(t *testing.T) {
	w := newIdleTestEndpoint()
	var resumes int
	w.endpoint.SetResumeErrHookForTest(func() error {
		resumes++
		return nil
	})
	if err := w.SetEnabled(true); err != nil || resumes != 0 {
		t.Fatalf("enable of an enabled endpoint is a no-op: err=%v resumes=%d", err, resumes)
	}
	_ = w.SetEnabled(false)
	sleepSince := w.sleepSince.Load()
	time.Sleep(time.Millisecond)
	if err := w.SetEnabled(false); err != nil || w.sleepSince.Load() != sleepSince {
		t.Fatal("a second disable changes nothing")
	}
	_ = w.SetEnabled(true)
	_ = w.SetEnabled(true)
	if resumes != 1 {
		t.Fatalf("a second enable must not wake again, resumes=%d", resumes)
	}
}

func TestSetEnabled_afterClose(t *testing.T) {
	w := newIdleTestEndpoint()
	_ = w.Close()
	if err := w.SetEnabled(false); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("disable after Close: got %v", err)
	}
	if err := w.SetEnabled(true); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("enable after Close: got %v", err)
	}
}

// The idle tick may still free a disabled endpoint's device.
func TestTeardownIfSlept_disabled(t *testing.T) {
	w := newIdleTestEndpoint()
	_ = w.SetEnabled(false)
	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(time.Minute)
	if !w.torndown.Load() {
		t.Fatal("a disabled endpoint asleep past the window is torn down")
	}
	assertState(t, w, adapter.EndpointStateDisabled)
	assertDisabledRefusal(t, w)
}

// Enable of a torn-down endpoint builds nothing; the next dial does.
func TestSetEnabled_enableTornDownRebuildsOnDial(t *testing.T) {
	counter := countDeviceBuilds(t)
	w := newLazyTestEndpoint(t, "wg-toggle", nil)
	startLazy(t, w)
	dialOnce(w)
	if counter.builds.Load() != 1 {
		t.Fatal("precondition: one build")
	}
	if err := w.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if !w.endpoint.Suspended() {
		t.Fatal("disable must bring the device down")
	}
	w.sleepSince.Store(time.Now().Add(-time.Hour).UnixNano())
	w.TeardownIfSlept(time.Minute)
	if counter.live.Load() != 0 {
		t.Fatal("teardown of a disabled endpoint closes the device")
	}
	assertDisabledRefusal(t, w)
	if counter.builds.Load() != 1 {
		t.Fatal("a refused dial must not rebuild")
	}
	if err := w.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	assertState(t, w, adapter.EndpointStateTornDown)
	if counter.builds.Load() != 1 {
		t.Fatal("enable of a torn-down endpoint builds nothing")
	}
	dialOnce(w)
	if counter.builds.Load() != 2 {
		t.Fatalf("the next dial rebuilds, builds=%d", counter.builds.Load())
	}
	assertState(t, w, adapter.EndpointStateUp)
}

// Enable of a never-built lazy endpoint leaves it for the first dial.
func TestSetEnabled_neverBuilt(t *testing.T) {
	counter := countDeviceBuilds(t)
	w := newLazyTestEndpoint(t, "wg-toggle", nil)
	startLazy(t, w)
	_ = w.SetEnabled(false)
	assertState(t, w, adapter.EndpointStateDisabled)
	dialOnce(w)
	if counter.builds.Load() != 0 {
		t.Fatal("a disabled never-built endpoint must not be built by a dial")
	}
	_ = w.SetEnabled(true)
	assertState(t, w, adapter.EndpointStateNeverBuilt)
	dialOnce(w)
	if counter.builds.Load() != 1 {
		t.Fatal("the first dial after enable builds the device")
	}
}

// A disabled endpoint with a built device is a budget victim like any sleeper.
func TestBuildBudget_evictsDisabled(t *testing.T) {
	budget := newTestBudget(t, 1, refsReporter{"a": {1, 0}})
	a := builtEndpoint(budget, "a", time.Second, true)
	if err := a.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	b := requester(budget, "b")
	if err := acquireWithin(t, budget, b, time.Second); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !a.torndown.Load() {
		t.Fatal("the disabled endpoint must be evicted")
	}
	assertState(t, a, adapter.EndpointStateDisabled)
	assertDisabledRefusal(t, a)
}
