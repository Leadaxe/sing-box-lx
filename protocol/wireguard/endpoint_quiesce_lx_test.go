// lx: SPEC 030 — the protocol side of the shutdown pre-pass. The nil-device
// transport endpoint makes the socket release a no-op; what is pinned here is
// the closing gate and that the call reaches the transport layer.
package wireguard

import (
	"os"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
)

func newQuiesceTestEndpoint() *Endpoint {
	w := &Endpoint{
		Adapter:  endpoint.NewAdapter(C.TypeWireGuard, "wg-quiesce", []string{N.NetworkTCP}, nil),
		logger:   logger.NOP(),
		endpoint: &wireguard.Endpoint{},
	}
	w.started.Store(true)
	return w
}

// The pre-pass marks the endpoint closing before Close runs, so every wake path
// (resumeOnDial fast path, Start) refuses from that point on.
func TestQuiesceForShutdown_setsClosingGate(t *testing.T) {
	w := newQuiesceTestEndpoint()
	w.QuiesceForShutdown()
	if !w.closing.Load() {
		t.Fatal("QuiesceForShutdown must set the closing gate")
	}
	if w.resumeOnDial(t.Context()) {
		t.Fatal("a quiesced endpoint must not be dialable")
	}
	if err := w.Start(adapter.StartStateStart, adapter.NewScope(t.Context(), logger.NOP())); err != os.ErrClosed {
		t.Fatalf("Start after the pre-pass must refuse with os.ErrClosed, got %v", err)
	}
	// Close after the pre-pass is the normal teardown.
	if err := w.Close(); err != nil {
		t.Fatalf("Close after QuiesceForShutdown: %v", err)
	}
}

// Nothing in the protocol layer waits: the pre-pass hands the socket release
// to the transport (its pauseSeq stamp moves) and returns.
func TestQuiesceForShutdown_reachesTransport(t *testing.T) {
	w := newQuiesceTestEndpoint()
	w.QuiesceForShutdown()
	if w.endpoint.PauseSeqForTest() == 0 {
		t.Fatal("QuiesceForShutdown must reach the transport endpoint")
	}
}
