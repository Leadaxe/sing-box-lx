package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

// GetWireGuardStatus returns the status snapshot of one WG/AWG endpoint
// (SPEC 114): device state, idle time and per-peer handshake, address and
// transfer. The call never builds or wakes a device. Errors: NotFound for an
// unknown tag, InvalidArgument for an endpoint of another type,
// FailedPrecondition while the service is not started.
func (c *CommandClient) GetWireGuardStatus(tag string) (*WireGuardEndpointStatus, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*WireGuardEndpointStatus, error) {
		response, err := client.GetWireGuardStatus(ctx, &daemon.WireGuardStatusRequest{Tag: tag})
		if err != nil {
			return nil, E.Cause(err, "get wireguard status")
		}
		return wireGuardEndpointStatusFromGRPC(response), nil
	})
}
