//go:build with_lx_command

package daemon

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 114 — status snapshot of one WG/AWG endpoint: device state, idle
// time and per-peer handshake/endpoint/transfer in one answer. Mirrors the
// GetTailscaleStatus getter (SPEC 115): one unary status per endpoint type,
// so GetOutbounds stays a plain node list.

// GetWireGuardStatus never builds or wakes a device: an endpoint without one
// answers with an empty peer list and the endpointState that explains why.
func (s *StartedService) GetWireGuardStatus(ctx context.Context, request *WireGuardStatusRequest) (*WireGuardEndpointStatus, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	endpoint, loaded := boxService.endpointManager.Get(request.Tag)
	if !loaded {
		return nil, status.Error(codes.NotFound, "endpoint not found: "+request.Tag)
	}
	// WG and AWG share one endpoint implementation; the reporter interface is
	// the type test, not the type string.
	reporter, isReporter := endpoint.(adapter.PeerStatusReporter)
	if !isReporter {
		return nil, status.Error(codes.InvalidArgument, "endpoint is not WireGuard: "+request.Tag)
	}
	response := &WireGuardEndpointStatus{
		EndpointTag: endpoint.Tag(),
		Peers:       peerStatusesToGRPC(reporter.PeerStatuses()),
	}
	if stateReporter, isStateReporter := endpoint.(adapter.IdleStateReporter); isStateReporter {
		idleState := stateReporter.IdleState()
		response.EndpointState = idleState.State
		response.IdleSinceSeconds = int64(idleState.IdleSince / time.Second)
	}
	return response, nil
}

// peerStatusesToGRPC — a zero handshake time maps to 0 ("no handshake yet"),
// not to a negative Unix value.
func peerStatusesToGRPC(statuses []adapter.PeerStatus) []*PeerStatus {
	if len(statuses) == 0 {
		return nil
	}
	peers := make([]*PeerStatus, 0, len(statuses))
	for _, status := range statuses {
		var lastHandshake int64
		if !status.LastHandshake.IsZero() {
			lastHandshake = status.LastHandshake.Unix()
		}
		peers = append(peers, &PeerStatus{
			PublicKey:         status.PublicKey,
			Endpoint:          status.Endpoint,
			LastHandshakeUnix: lastHandshake,
			RxBytes:           int64(status.RxBytes),
			TxBytes:           int64(status.TxBytes),
		})
	}
	return peers
}
