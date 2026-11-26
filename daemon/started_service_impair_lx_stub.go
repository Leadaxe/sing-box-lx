//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_impair_lx.go (SPEC 164).
func (s *StartedService) SetImpair(ctx context.Context, request *SetImpairRequest) (*ImpairStatus, error) {
	return nil, status.Error(codes.Unimplemented, "SetImpair is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) ScheduleImpair(ctx context.Context, request *ScheduleImpairRequest) (*ImpairStatus, error) {
	return nil, status.Error(codes.Unimplemented, "ScheduleImpair is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetImpairStatus(ctx context.Context, request *ImpairStatusRequest) (*ImpairStatus, error) {
	return nil, status.Error(codes.Unimplemented, "GetImpairStatus is not included in this build, rebuild with -tags with_lx_command")
}
