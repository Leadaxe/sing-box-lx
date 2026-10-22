package wireguard

import "github.com/sagernet/sing-box/adapter"

var _ adapter.ShutdownQuiescer = (*Endpoint)(nil) // lx: SPEC 030 fast shutdown

// QuiesceForShutdown is the box.Close pre-pass for one endpoint (SPEC 030):
// mark it closing, so an in-flight or later wake (resumeOnDial, Start, a build
// waiting on the budget) gives up instead of rebuilding a device the close pass
// is about to tear down, and release the transport socket ahead of that pass.
// Close still runs the full teardown afterwards; this only moves the socket
// close forward and keeps the wake paths out of the way.
func (w *Endpoint) QuiesceForShutdown() {
	w.closing.Store(true)
	w.budget.Notify() // lx: SPEC 097 — a build waiting on the budget sees closing and gives up
	w.endpoint.QuiesceForShutdown()
}
