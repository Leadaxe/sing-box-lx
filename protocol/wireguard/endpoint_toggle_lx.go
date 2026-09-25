package wireguard

// lx: SPEC 106 — manual enable/disable of the endpoint.
//
// Disable reuses the SPEC 020 sleep: an awake endpoint is suspended exactly as
// the idle tick would do it, and the disabled flag keeps every dial from
// waking it. The idle tick may still tear a disabled endpoint down, and the
// build budget may evict it: both only free resources. Enable wakes a
// suspended device eagerly; a torn-down or never-built one is rebuilt by the
// next dial, through the budget, as usual. Not persisted: a new box (reload,
// apply) starts every endpoint enabled.

import (
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

var _ adapter.EndpointToggle = (*Endpoint)(nil)

var (
	errEndpointDisabled = E.New("WireGuard endpoint is disabled")
	errEndpointNotReady = E.New("WireGuard is not ready yet")
)

// notReadyError is the error of a dial the endpoint refused (resumeOnDial
// returned false).
func (w *Endpoint) notReadyError() error {
	if w.disabled.Load() {
		return errEndpointDisabled
	}
	return errEndpointNotReady
}

// Enabled implements adapter.EndpointToggle.
func (w *Endpoint) Enabled() bool {
	return !w.disabled.Load()
}

// SetEnabled implements adapter.EndpointToggle. Idempotent. Returns
// os.ErrClosed on a closing endpoint, and the device.Up() error when an eager
// wake fails (the endpoint is enabled but stays asleep; the next dial retries
// the wake, as in resumeOnDial).
func (w *Endpoint) SetEnabled(enabled bool) error {
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.closing.Load() {
		return os.ErrClosed
	}
	if !enabled {
		if !w.disabled.CompareAndSwap(false, true) {
			return nil
		}
		// A dial that read disabled == false before the store may still be
		// running on the device: Down cuts it, as it cuts every established
		// flow. That is the point of the switch.
		if w.started.Load() {
			w.started.Store(false)
			w.sleepSince.Store(time.Now().UnixNano())
			w.idleAsleep.Store(true)
			w.endpoint.Suspend()
			w.budget.Notify()
		}
		w.logger.Info("lx endpoint: disable ", w.Tag())
		return nil
	}
	if !w.disabled.CompareAndSwap(true, false) {
		return nil
	}
	w.logger.Info("lx endpoint: enable ", w.Tag())
	if !w.idleAsleep.Load() || w.torndown.Load() || w.neverBuilt.Load() {
		// Awake already (disabled before Start finished), stopped, or without a
		// device: the next dial builds it through the budget.
		return nil
	}
	if err := w.endpoint.Resume(); err != nil {
		w.logger.Error("lx endpoint: wake ", w.Tag(), " failed: ", err)
		return err
	}
	w.started.Store(true)
	w.idleAsleep.Store(false)
	w.sleepSince.Store(0)
	return nil
}
