package wireguard

// lx: SPEC 020 on the 1.15 line — upstream's on_demand suspend and the
// transport's wake go through the endpoint's sleep state.

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

func TestOnDemand_suspendAndWakeAreAccounted(t *testing.T) {
	w := newIdleTestEndpoint()
	w.SetKeepIdleConnections(false)
	if w.started.Load() || !w.idleAsleep.Load() || w.sleepSince.Load() == 0 || !w.endpoint.Suspended() {
		t.Fatal("on_demand suspend: device down, idleAsleep, sleep clock stamped")
	}
	assertState(t, w, adapter.EndpointStateAsleep)

	w.SetKeepIdleConnections(true)
	if !w.started.Load() || w.idleAsleep.Load() || w.sleepSince.Load() != 0 || w.endpoint.Suspended() {
		t.Fatal("on_demand wake: device up, state cleared")
	}
	assertState(t, w, adapter.EndpointStateUp)
}

// The reference manager's "keep" must not wake what something else put to
// sleep: an idle-suspended endpoint waits for traffic, a disabled one for the
// switch.
func TestOnDemand_keepWakesOnlyItsOwnSleep(t *testing.T) {
	idle := newIdleTestEndpoint()
	idle.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	idle.SuspendIfIdle(false, time.Minute, 0)
	idle.SetKeepIdleConnections(true)
	assertState(t, idle, adapter.EndpointStateAsleep)

	disabled := newIdleTestEndpoint()
	disabled.SetKeepIdleConnections(false)
	if err := disabled.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	disabled.SetKeepIdleConnections(true)
	assertState(t, disabled, adapter.EndpointStateDisabled)
	if !disabled.endpoint.Suspended() {
		t.Fatal("a disabled endpoint must stay down through an on_demand wake")
	}
}

func TestOnDemand_stateGates(t *testing.T) {
	listening := newIdleTestEndpoint()
	listening.listenMode = true
	listening.SetKeepIdleConnections(false)
	assertState(t, listening, adapter.EndpointStateUp)

	disabled := newIdleTestEndpoint()
	if err := disabled.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	disabled.SetKeepIdleConnections(false)
	if disabled.onDemandAsleep.Load() {
		t.Fatal("on_demand does not claim an endpoint that is already down")
	}
}

// A packet reaching a suspended device used to bring it up inside the
// transport, past the state: the endpoint ran while reporting asleep, and the
// idle tick, which only suspends on the awake→asleep transition, never put it
// back. A disabled endpoint woke the same way.
func TestTransportWake_goesThroughState(t *testing.T) {
	w := newIdleTestEndpoint()
	w.endpoint.SetWakeHookLx(w.wakeByTrafficLx)
	w.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	w.SuspendIfIdle(false, time.Minute, 0)
	assertState(t, w, adapter.EndpointStateAsleep)

	w.wakeByTrafficLx()
	if !w.started.Load() || w.idleAsleep.Load() || w.endpoint.Suspended() {
		t.Fatal("a packet wake must clear the sleep state with the device")
	}
	assertState(t, w, adapter.EndpointStateUp)
	if w.IdleSince() > time.Minute {
		t.Fatal("a packet wake stamps activity: the next tick must not suspend at once")
	}

	disabled := newIdleTestEndpoint()
	if err := disabled.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	disabled.wakeByTrafficLx()
	assertState(t, disabled, adapter.EndpointStateDisabled)
	if !disabled.endpoint.Suspended() {
		t.Fatal("a packet must not wake a disabled endpoint")
	}

	torn := newIdleTestEndpoint()
	torn.started.Store(false)
	torn.idleAsleep.Store(true)
	torn.torndown.Store(true)
	torn.wakeByTrafficLx()
	if torn.started.Load() {
		t.Fatal("a torn-down endpoint is rebuilt by a dial, not by a packet")
	}
}
