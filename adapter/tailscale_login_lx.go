package adapter

import (
	"context"

	E "github.com/sagernet/sing/common/exceptions"
)

// lx: SPEC 149 — login with a new auth key and on/off switch of a Tailscale
// endpoint without restarting the box. The switch is EndpointToggle (SPEC 106).

var (
	// ErrTailscaleDisabled: the endpoint is switched off by EndpointToggle.
	ErrTailscaleDisabled = E.New("Tailscale endpoint is disabled")
	// ErrTailscaleLoggedIn: login was asked of a node that is already logged
	// in; the auth key would be ignored, so the caller logs out first.
	ErrTailscaleLoggedIn = E.New("Tailscale is already logged in, log out first")
	// ErrTaildropDisabled: Taildrop is switched off by disable_taildrop.
	ErrTaildropDisabled = E.New("Taildrop is disabled on this node")
)

// TailscaleLoginProvider is implemented by the Tailscale endpoint. Login
// restarts the backend with the given auth key the way the endpoint start
// does; an empty key starts an interactive login (AuthURL in the status).
// A rejected key is reported asynchronously through the backend state and
// health, as at start. os.ErrClosed while the endpoint is not started.
type TailscaleLoginProvider interface {
	TailscaleLogin(ctx context.Context, authKey string) error
}
