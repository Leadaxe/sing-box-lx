//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_tailscale_lx.go (SPEC 115, CONSTITUTION §3.6 pt.3).
func (s *StartedService) GetTailscaleStatus(ctx context.Context, request *TailscaleStatusRequest) (*TailscaleEndpointStatus, error) {
	return nil, status.Error(codes.Unimplemented, "GetTailscaleStatus is not included in this build, rebuild with -tags with_lx_command")
}
