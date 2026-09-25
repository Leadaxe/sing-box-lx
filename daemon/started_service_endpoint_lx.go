//go:build with_lx_command

package daemon

import (
	"context"
	"errors"
	"os"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetEndpointEnabled — SPEC 106: manual on/off switch of a WG/AWG endpoint, a
// bridge to adapter.EndpointToggle. The response carries the endpoint state
// after the call, the same string GetOutbounds reports.
func (s *StartedService) SetEndpointEnabled(ctx context.Context, request *SetEndpointEnabledRequest) (*SetEndpointEnabledResponse, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	endpoint, loaded := boxService.endpointManager.Get(request.Tag)
	if !loaded {
		return nil, status.Error(codes.NotFound, "endpoint not found: "+request.Tag)
	}
	toggle, isToggle := endpoint.(adapter.EndpointToggle)
	if !isToggle {
		return nil, status.Error(codes.InvalidArgument, "endpoint cannot be switched on and off: "+request.Tag)
	}
	err := toggle.SetEnabled(request.Enabled)
	if errors.Is(err, os.ErrClosed) {
		return nil, status.Error(codes.FailedPrecondition, "endpoint is closing: "+request.Tag)
	} else if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	var response SetEndpointEnabledResponse
	if reporter, isReporter := endpoint.(adapter.IdleStateReporter); isReporter {
		response.State = reporter.IdleState().State
	}
	return &response, nil
}
