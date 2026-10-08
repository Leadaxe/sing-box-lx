//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_wireguard_lx.go (SPEC 114, CONSTITUTION §3.6 pt.3).
func (s *StartedService) GetWireGuardStatus(ctx context.Context, request *WireGuardStatusRequest) (*WireGuardEndpointStatus, error) {
	return nil, status.Error(codes.Unimplemented, "GetWireGuardStatus is not included in this build, rebuild with -tags with_lx_command")
}
