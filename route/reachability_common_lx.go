// lx:begin idle-suspend
package route

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// lxWG returns the resolved lx.wg values from the box context (SPEC 098), or
// all-off when the context carries none (tests, a router built outside
// box.New) or is nil.
func lxWG(ctx context.Context) option.LXWGResolved {
	if ctx == nil {
		return option.LXWGResolved{}
	}
	return service.PtrFromContext[option.LXResolved](ctx).WGOrZero()
}

// QuiesceForShutdown prepares the router for a fast box shutdown (SPEC 030),
// called at the top of box.Close before endpoints are torn down. It stops the
// idle-suspend tick (a no-op without the with_lx_idle_suspend tag) so no new
// reachability wake can enter an endpoint's resumeOnDial while it is closing,
// broadcasts a device-pause, which parks the pause-registered tickers (urltest
// health checks among them) for the rest of the shutdown, and then walks the
// endpoint manager calling QuiesceForShutdown on every endpoint that has one:
// each WG/AWG endpoint marks itself closing and releases its UDP/detour socket
// up front, so the later device.Close() drain (stopping.Wait) returns at once
// instead of waiting out a socket close per endpoint.
//
// The walk is the pre-pass. The broadcast used to be: the transport endpoint
// answered EventDevicePaused with device.Down() until upstream 6364d9a5f ("Fix
// WireGuard endpoint stopping on device sleep") removed that reaction so a
// WireGuard endpoint lives through device sleep — SPEC 020 depends on it, and
// it stays. Lives in the always-compiled file so it exists in every build.
// Idempotent and safe on every platform; the per-endpoint call returns at once
// (it kicks the socket release off, it does not wait for it).
func (r *Router) QuiesceForShutdown() {
	r.stopIdleSuspend()
	if r.pauseManager != nil {
		r.pauseManager.DevicePause()
	}
	if r.endpoint != nil {
		for _, endpoint := range r.endpoint.Endpoints() {
			if quiescer, isQuiescer := endpoint.(adapter.ShutdownQuiescer); isQuiescer {
				quiescer.QuiesceForShutdown()
			}
		}
	}
}

// lx:end idle-suspend
