//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Build-tag twin of started_service_chain_lx.go (SPEC 073, CONSTITUTION §3.6 pt.3).
func (s *StartedService) GetChains(ctx context.Context, empty *emptypb.Empty) (*ChainList, error) {
	return nil, status.Error(codes.Unimplemented, "GetChains is not included in this build, rebuild with -tags with_lx_command")
}
