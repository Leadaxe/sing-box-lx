//go:build with_lx_command

package daemon

import (
	"context"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetChainStatus — SPEC 073 / 165: состояние одной цепочки по тегу (позиции,
// выбранный узел, выключение, счётчики ошибок), унарно, как статусы
// endpoint'ов. Чистый мост к adapter.ChainStatusProvider.
func (s *StartedService) GetChainStatus(ctx context.Context, request *GetChainStatusRequest) (*ChainState, error) {
	detour, err := s.chainOutbound(request.Tag)
	if err != nil {
		return nil, err
	}
	provider, isChain := detour.(adapter.ChainStatusProvider)
	if !isChain {
		return nil, status.Error(codes.InvalidArgument, "outbound is not a chain: "+request.Tag)
	}
	return chainStateToGRPC(provider.ChainStatus()), nil
}

// SetChainPositionEnabled — SPEC 075: runtime toggle of one chain position.
func (s *StartedService) SetChainPositionEnabled(ctx context.Context, request *SetChainPositionEnabledRequest) (*SetChainPositionEnabledResponse, error) {
	detour, err := s.chainOutbound(request.ChainTag)
	if err != nil {
		return nil, err
	}
	controller, isChain := detour.(adapter.ChainController)
	if !isChain {
		return nil, status.Error(codes.InvalidArgument, "outbound is not a chain: "+request.ChainTag)
	}
	if err := controller.SetPositionEnabled(int(request.Position), request.Enabled); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &SetChainPositionEnabledResponse{}, nil
}

func (s *StartedService) chainOutbound(chainTag string) (adapter.Outbound, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	detour, loaded := boxService.outboundManager.Outbound(chainTag)
	if !loaded {
		return nil, status.Error(codes.NotFound, "outbound not found: "+chainTag)
	}
	return detour, nil
}

func chainStateToGRPC(chainStatus adapter.ChainStatus) *ChainState {
	state := &ChainState{
		Tag:    chainStatus.Tag,
		Dials:  chainStatus.Dials,
		Errors: chainStatus.Errors,
	}
	for _, position := range chainStatus.Positions {
		state.Positions = append(state.Positions, &ChainPosition{
			Tag:         position.Tag,
			IsGroup:     position.IsGroup,
			Now:         position.Now,
			Transparent: position.Transparent,
			Disabled:    position.Disabled,
			Errors:      position.Errors,
		})
	}
	return state
}
