//go:build !with_lx_command

package daemon

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build-tag twin of started_service_conntest_lx.go (SPEC 139, CONSTITUTION §3.6 pt.3).

func (s *StartedService) StartConnectionTest(request *ConnectionTestRequest, server grpc.ServerStreamingServer[ConnectionTestProgress]) error {
	return status.Error(codes.Unimplemented, "StartConnectionTest is not included in this build, rebuild with -tags with_lx_command")
}
