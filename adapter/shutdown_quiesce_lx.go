package adapter

// lx: SPEC 030 — fast box shutdown.
//
// ShutdownQuiescer is implemented by endpoints that can release their network
// sockets ahead of the close pass. box.Close calls it on every endpoint (through
// Router.QuiesceForShutdown) before the scopes tear the endpoints down, so each
// device's close drain finds its receive workers already gone instead of
// waiting out a socket close of its own, one endpoint after another.
//
// The call must return promptly: it marks the endpoint as closing and kicks the
// socket release off, it does not wait for it. Close still runs the full
// teardown afterwards — nothing is skipped, only moved forward.
type ShutdownQuiescer interface {
	QuiesceForShutdown()
}
