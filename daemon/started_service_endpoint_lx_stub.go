//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_endpoint_lx.go (SPEC 106, CONSTITUTION §3.6 pt.3).
func (s *StartedService) SetEndpointEnabled(ctx context.Context, request *SetEndpointEnabledRequest) (*SetEndpointEnabledResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SetEndpointEnabled is not included in this build, rebuild with -tags with_lx_command")
}
