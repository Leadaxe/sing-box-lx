//go:build with_gvisor

package tailscale

// lx: SPEC 149 — on/off switch (adapter.EndpointToggle, SPEC 106) and login
// with a new auth key, without restarting the box.
//
// Disable sets the refusal flag first and then stops the backend with
// WantRunning=false: the control connection and the magicsock peers go away,
// the node identity and state stay. Enable runs the backend again and clears
// the flag after it, so dials are refused through the whole transition. Not
// persisted: tsnet passes WantRunning=true in UpdatePrefs on every start, which
// overrides the stored value, and the flag lives in memory only.

import (
	"context"
	"os"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/tailscale/ipn"
)

var (
	_ adapter.EndpointToggle         = (*Endpoint)(nil)
	_ adapter.IdleStateReporter      = (*Endpoint)(nil)
	_ adapter.TailscaleLoginProvider = (*Endpoint)(nil)
)

func (t *Endpoint) SetEnabled(enabled bool) error {
	t.toggleAccess.Lock()
	defer t.toggleAccess.Unlock()
	if !t.started.Load() {
		return os.ErrClosed
	}
	if enabled != t.disabled.Load() {
		return nil
	}
	if !enabled {
		t.disabled.Store(true)
	}
	_, err := t.server.ExportLocalBackend().EditPrefs(&ipn.MaskedPrefs{
		Prefs:          ipn.Prefs{WantRunning: enabled},
		WantRunningSet: true,
	})
	if err != nil {
		return E.Cause(err, "update prefs")
	}
	if enabled {
		t.disabled.Store(false)
	}
	return nil
}

func (t *Endpoint) Enabled() bool {
	return !t.disabled.Load()
}

// IdleState reports the switch level only; the backend state (NeedsLogin,
// Running, Stopped) is in the Tailscale status (SPEC 115).
func (t *Endpoint) IdleState() adapter.IdleState {
	switch {
	case !t.started.Load():
		return adapter.IdleState{State: adapter.EndpointStateDown}
	case t.disabled.Load():
		return adapter.IdleState{State: adapter.EndpointStateDisabled}
	default:
		return adapter.IdleState{State: adapter.EndpointStateUp}
	}
}

// TailscaleLogin repeats the login bootstrap of tsnet.Server.start on the
// running backend: Start with the auth key and the start prefs, then an
// interactive login while the backend needs one. tsnet ignores a key once the
// node is logged in, so a logged-in node is refused instead of being logged
// out implicitly.
func (t *Endpoint) TailscaleLogin(ctx context.Context, authKey string) error {
	t.toggleAccess.Lock()
	defer t.toggleAccess.Unlock()
	if !t.started.Load() {
		return os.ErrClosed
	}
	if t.disabled.Load() {
		return adapter.ErrTailscaleDisabled
	}
	localBackend := t.server.ExportLocalBackend()
	switch localBackend.State() {
	case ipn.Running, ipn.Starting:
		return adapter.ErrTailscaleLoggedIn
	}
	prefs := ipn.NewPrefs()
	prefs.Hostname = t.server.Hostname
	prefs.WantRunning = true
	prefs.ControlURL = t.server.ControlURL
	prefs.AdvertiseTags = t.server.AdvertiseTags
	err := localBackend.Start(ipn.Options{UpdatePrefs: prefs, AuthKey: authKey})
	if err != nil {
		return E.Cause(err, "restart backend")
	}
	err = t.editPrefs(t.sshServerInstance != nil)
	if err != nil {
		return err
	}
	if localBackend.State() == ipn.NeedsLogin {
		err = localBackend.StartLoginInteractive(ctx)
		if err != nil {
			return E.Cause(err, "start login")
		}
	}
	return nil
}

func (t *Endpoint) checkEnabled() error {
	if t.disabled.Load() {
		return adapter.ErrTailscaleDisabled
	}
	return nil
}
