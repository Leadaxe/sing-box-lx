//go:build with_lx_command

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// impairControl is what an `impair` outbound (protocol/impair, SPEC 164)
// offers to the runtime commands. Matched by interface, so this package does
// not import the outbound: in a build without with_lx_impair no outbound has
// it and the commands answer InvalidArgument.
type impairControl interface {
	ImpairSet(entry []byte) error
	ImpairSchedule(entries []byte, start string, repeat string) error
	ImpairStatus() ([]byte, error)
}

func (s *StartedService) impairOutbound(tag string) (impairControl, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()
	outbound, loaded := boxService.outboundManager.Outbound(tag)
	if !loaded {
		return nil, status.Error(codes.NotFound, "outbound not found: "+tag)
	}
	control, isImpair := outbound.(impairControl)
	if !isImpair {
		return nil, status.Error(codes.InvalidArgument, "not an impair outbound: "+tag)
	}
	return control, nil
}

func impairStatus(control impairControl) (*ImpairStatus, error) {
	content, err := control.ImpairStatus()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &ImpairStatus{Json: string(content)}, nil
}

// SetImpair — SPEC 164: apply one change of an impair outbound's link now
// (a schedule entry without "at"), on top of its running schedule.
func (s *StartedService) SetImpair(ctx context.Context, request *SetImpairRequest) (*ImpairStatus, error) {
	control, err := s.impairOutbound(request.OutboundTag)
	if err != nil {
		return nil, err
	}
	if err = control.ImpairSet([]byte(request.Entry)); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return impairStatus(control)
}

// ScheduleImpair — SPEC 164: replace an impair outbound's schedule; the link
// goes back to the config first. [] only restores the config's link.
func (s *StartedService) ScheduleImpair(ctx context.Context, request *ScheduleImpairRequest) (*ImpairStatus, error) {
	control, err := s.impairOutbound(request.OutboundTag)
	if err != nil {
		return nil, err
	}
	if err = control.ImpairSchedule([]byte(request.Entries), request.Start, request.Repeat); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return impairStatus(control)
}

// GetImpairStatus — SPEC 164: the running state of an impair outbound.
func (s *StartedService) GetImpairStatus(ctx context.Context, request *ImpairStatusRequest) (*ImpairStatus, error) {
	control, err := s.impairOutbound(request.OutboundTag)
	if err != nil {
		return nil, err
	}
	return impairStatus(control)
}
