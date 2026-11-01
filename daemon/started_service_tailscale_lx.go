//go:build with_lx_command

package daemon

import (
	"context"
	"errors"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/experimental/locale"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// lx: SPEC 115 — on-demand status of one Tailscale endpoint: the same message
// SubscribeTailscaleStatus streams, with a fresh backend snapshot (peer paths,
// handshakes, health). Mirrors GetWireGuardStatus (SPEC 114).

func (s *StartedService) GetTailscaleStatus(ctx context.Context, request *TailscaleStatusRequest) (*TailscaleEndpointStatus, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	endpoint, err := resolveTailscaleEndpoint(boxService, request.EndpointTag)
	if err != nil {
		return nil, err
	}
	provider, isProvider := endpoint.(adapter.TailscaleStatusProvider)
	if !isProvider {
		return nil, status.Error(codes.InvalidArgument, "endpoint is not Tailscale: "+request.EndpointTag)
	}
	endpointStatus, err := provider.TailscaleStatus()
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return tailscaleEndpointStatusToProto(endpoint.Tag(), endpointStatus, locale.FromContext(ctx)), nil
}

// TailscaleLogin — SPEC 149: login of a Tailscale endpoint with a new auth key
// on the running backend, no box restart. The key never reaches a log or a
// response.
func (s *StartedService) TailscaleLogin(ctx context.Context, request *TailscaleLoginRequest) (*emptypb.Empty, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	endpoint, err := resolveTailscaleEndpoint(boxService, request.EndpointTag)
	if err != nil {
		return nil, err
	}
	provider, isProvider := endpoint.(adapter.TailscaleLoginProvider)
	if !isProvider {
		return nil, status.Error(codes.InvalidArgument, "endpoint is not Tailscale: "+request.EndpointTag)
	}
	err = provider.TailscaleLogin(ctx, request.AuthKey)
	switch {
	case err == nil:
		return &emptypb.Empty{}, nil
	case errors.Is(err, os.ErrClosed):
		return nil, status.Error(codes.FailedPrecondition, "endpoint is not started: "+request.EndpointTag)
	case errors.Is(err, adapter.ErrTailscaleDisabled), errors.Is(err, adapter.ErrTailscaleLoggedIn):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	default:
		return nil, status.Error(codes.Unavailable, err.Error())
	}
}
