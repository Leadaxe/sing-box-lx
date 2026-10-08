package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

// lx: SPEC 115 — on-demand Tailscale status.

// GetTailscaleStatus returns the status of one Tailscale endpoint — the same
// object SubscribeTailscaleStatus delivers — read fresh from the backend, so
// per-peer Path / Endpoint / PeerRelay / DERPRegionCode / LastHandshake and
// Health() are current at call time. Errors: NotFound for an unknown tag,
// InvalidArgument for an endpoint of another type, FailedPrecondition while
// the service or the endpoint is not started.
func (c *CommandClient) GetTailscaleStatus(endpointTag string) (*TailscaleEndpointStatus, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*TailscaleEndpointStatus, error) {
		response, err := client.GetTailscaleStatus(ctx, &daemon.TailscaleStatusRequest{EndpointTag: endpointTag})
		if err != nil {
			return nil, E.Cause(err, "get tailscale status")
		}
		return tailscaleEndpointStatusFromGRPC(response), nil
	})
}

func tailscalePeerPathFromGRPC(path daemon.TailscalePeerPath) string {
	switch path {
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT:
		return "direct"
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY:
		return "peer_relay"
	case daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DERP:
		return "derp"
	default:
		return ""
	}
}
