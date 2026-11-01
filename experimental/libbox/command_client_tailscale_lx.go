package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/protobuf/types/known/emptypb"
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

// TailscaleLogin logs a Tailscale endpoint in with a new auth key without
// restarting the service (SPEC 149); an empty key starts an interactive login
// with the AuthURL in the status. A rejected key shows in the backend state
// and Health() of the status, as at start. Errors: FailedPrecondition for a
// node already logged in (log out first), a disabled node
// (SetEndpointEnabled) or an endpoint not started; NotFound for an unknown
// tag, InvalidArgument for an endpoint of another type.
func (c *CommandClient) TailscaleLogin(endpointTag string, authKey string) error {
	_, err := callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*emptypb.Empty, error) {
		return client.TailscaleLogin(ctx, &daemon.TailscaleLoginRequest{EndpointTag: endpointTag, AuthKey: authKey})
	})
	if err != nil {
		return E.Cause(err, "tailscale login")
	}
	return nil
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
