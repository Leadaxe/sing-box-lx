// lx: SPEC 030 — Router.QuiesceForShutdown must reach every endpoint that can
// release its socket ahead of the close pass. The broadcast it used to rely on
// (DevicePause → device.Down) is gone upstream (6364d9a5f), so the walk over
// the endpoint manager is the only thing that closes sockets up front.
package route

import (
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

type quiesceEndpointManager struct {
	adapter.EndpointManager
	endpoints []adapter.Endpoint
}

func (m *quiesceEndpointManager) Endpoints() []adapter.Endpoint { return m.endpoints }

// quiesceStub is a WG/AWG stand-in: an endpoint that implements
// adapter.ShutdownQuiescer and counts the calls.
type quiesceStub struct {
	adapter.Endpoint
	calls int
}

func (s *quiesceStub) QuiesceForShutdown() { s.calls++ }

// plainStub is an endpoint without a pre-pass (masque, tailscale, …).
type plainStub struct {
	adapter.Endpoint
}

func TestQuiesceForShutdownWalksEndpoints(t *testing.T) {
	first := &quiesceStub{}
	second := &quiesceStub{}
	router := &Router{
		endpoint: &quiesceEndpointManager{endpoints: []adapter.Endpoint{first, &plainStub{}, second}},
	}
	router.QuiesceForShutdown()
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("every quiescable endpoint must be reached exactly once, got %d and %d", first.calls, second.calls)
	}
}

// A router built without an endpoint manager or pause manager (tests, a router
// outside box.New) quiesces as a no-op.
func TestQuiesceForShutdownWithoutManagers(t *testing.T) {
	router := &Router{}
	router.QuiesceForShutdown()
}
