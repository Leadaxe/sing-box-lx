// lx: SPEC 070 — Start/Close race regression. The daemon may run Box.Close
// concurrently with a still-running Box.Start (stop pressed during a slow
// start); once Close has run, a Start stage must refuse instead of entering
// the transport over a nil tun device. On the pre-fix base this test dies
// with a nil dereference inside the transport Start (the field crash's
// unguarded entry) instead of the clean os.ErrClosed.
package wireguard

import (
	"errors"
	"os"
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// Close first, Start after: the post-close ordering of the field crash. Both
// stages must refuse at the closing gate without touching the transport
// (whose tunDevice is already nil at that point).
func TestStartRefusedAfterClose(t *testing.T) {
	w := newIdleTestEndpoint()
	if err := w.Close(); err != nil {
		t.Fatalf("Close of a nil-device endpoint must not error: %v", err)
	}
	for _, stage := range []adapter.StartStage{adapter.StartStateStart, adapter.StartStatePostStart} {
		err := w.Start(stage)
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Start(stage %d) after Close: want os.ErrClosed, got %v", stage, err)
		}
	}
	if w.started.Load() {
		t.Fatal("a refused Start must not mark the endpoint started")
	}
}
