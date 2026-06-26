//go:build !with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Build-tag twin of started_service_command_lx.go (SPEC 014, CONSTITUTION §3.6 pt.3).
// Without with_lx_command the RPCs are still registered by the generated service (the
// .proto seam is always compiled) but answered with codes.Unimplemented, so a tag-less
// sing-box/libbox is behaviourally equivalent to upstream.

func (s *StartedService) URLTestOutbound(ctx context.Context, request *URLTestOutboundRequest) (*URLTestOutboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "URLTestOutbound is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetRules(ctx context.Context, empty *emptypb.Empty) (*RuleList, error) {
	return nil, status.Error(codes.Unimplemented, "GetRules is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetGroups(ctx context.Context, empty *emptypb.Empty) (*Groups, error) {
	return nil, status.Error(codes.Unimplemented, "GetGroups is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) GetOutbounds(ctx context.Context, empty *emptypb.Empty) (*OutboundList, error) {
	return nil, status.Error(codes.Unimplemented, "GetOutbounds is not included in this build, rebuild with -tags with_lx_command")
}

func (s *StartedService) SubscribeDNSQueries(request *SubscribeDNSQueriesRequest, server grpc.ServerStreamingServer[DnsQueryEvent]) error {
	return status.Error(codes.Unimplemented, "SubscribeDNSQueries is not included in this build, rebuild with -tags with_lx_command")
}
