//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_chain_lx.go (SPEC 073/075/165, CONSTITUTION §3.6 pt.3).
func (s *StartedService) GetChainStatus(ctx context.Context, request *GetChainStatusRequest) (*ChainState, error) {
	return nil, status.Error(codes.Unimplemented, "GetChainStatus is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) SetChainPositionEnabled(ctx context.Context, request *SetChainPositionEnabledRequest) (*SetChainPositionEnabledResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SetChainPositionEnabled is not included in this build, rebuild with -tags with_lx_command")
}
