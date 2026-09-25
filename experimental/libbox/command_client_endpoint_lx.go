package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

// EndpointToggleResult — SPEC 106. An object (not a bare string) crosses the
// gomobile bridge — see RunningConfig for why. State is the endpoint state after
// the call, the same value GetOutbounds reports in OutboundGroupItem.EndpointState
// ("disabled" after a disable, "up" after a successful enable of a sleeping node,
// "torn_down"/"never_built" when the device is built by the next dial).
type EndpointToggleResult struct {
	State string
}

// SetEndpointEnabled switches a WG/AWG endpoint on or off at runtime (SPEC 106).
// A disabled endpoint puts its device to sleep and rejects dials until enabled.
// Not persisted: a reload brings every endpoint back enabled.
func (c *CommandClient) SetEndpointEnabled(tag string, enabled bool) (*EndpointToggleResult, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*EndpointToggleResult, error) {
		response, err := client.SetEndpointEnabled(ctx, &daemon.SetEndpointEnabledRequest{
			Tag:     tag,
			Enabled: enabled,
		})
		if err != nil {
			return nil, E.Cause(err, "set endpoint enabled")
		}
		return &EndpointToggleResult{State: response.State}, nil
	})
}
