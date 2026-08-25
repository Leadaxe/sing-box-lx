//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Build-tag twin of started_service_chain_lx.go (SPEC 073/075, CONSTITUTION §3.6 pt.3).
func (s *StartedService) GetChains(ctx context.Context, empty *emptypb.Empty) (*ChainList, error) {
	return nil, status.Error(codes.Unimplemented, "GetChains is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) SetChainPositionEnabled(ctx context.Context, request *SetChainPositionEnabledRequest) (*SetChainPositionEnabledResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SetChainPositionEnabled is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetChainCloneConfig(ctx context.Context, request *GetChainCloneConfigRequest) (*RunningConfig, error) {
	return nil, status.Error(codes.Unimplemented, "GetChainCloneConfig is not included in this build, rebuild with -tags with_lx_command")
}
