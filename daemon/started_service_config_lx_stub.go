//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Build-tag twin of started_service_config_lx.go (SPEC 165).
func (s *StartedService) GetEffectiveConfig(ctx context.Context, empty *emptypb.Empty) (*RunningConfig, error) {
	return nil, status.Error(codes.Unimplemented, "GetEffectiveConfig is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetOutboundConfig(ctx context.Context, request *GetOutboundConfigRequest) (*OutboundConfig, error) {
	return nil, status.Error(codes.Unimplemented, "GetOutboundConfig is not included in this build, rebuild with -tags with_lx_command")
}
