//go:build with_lx_command

package daemon

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// GetChains — SPEC 073: состояние всех outbound'ов `chain` (позиции, разрешённый
// узел, звенья с их состоянием/MTU/strip/rewrite, счётчики). Чистый мост к
// adapter.ChainStatusProvider, который реализует цепочка; без `with_lx_chain`
// в конфиге цепочек нет и список пуст.
func (s *StartedService) GetChains(ctx context.Context, empty *emptypb.Empty) (*ChainList, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	now := time.Now()
	var list ChainList
	for _, detour := range boxService.outboundManager.Outbounds() {
		provider, isChain := detour.(adapter.ChainStatusProvider)
		if !isChain {
			continue
		}
		list.Chains = append(list.Chains, chainStateToGRPC(provider.ChainStatus(), now))
	}
	return &list, nil
}

func chainStateToGRPC(chainStatus adapter.ChainStatus, now time.Time) *ChainState {
	state := &ChainState{
		Tag:           chainStatus.Tag,
		Dials:         chainStatus.Dials,
		Errors:        chainStatus.Errors,
		ClonesCreated: chainStatus.ClonesCreated,
		ClonesEvicted: chainStatus.ClonesEvicted,
		LiveClones:    chainStatus.LiveClones,
	}
	for _, position := range chainStatus.Positions {
		item := &ChainPosition{
			Tag:         position.Tag,
			IsGroup:     position.IsGroup,
			Now:         position.Now,
			Transparent: position.Transparent,
			Errors:      position.Errors,
		}
		if position.Clone != nil {
			item.Clone = &ChainCloneState{
				State:         position.Clone.State,
				ActiveConns:   position.Clone.ActiveConns,
				MtuConfigured: position.Clone.MTUConfigured,
				MtuEffective:  position.Clone.MTUEffective,
				MtuReason:     position.Clone.MTUReason,
				Stripped:      position.Clone.Stripped,
				Rewritten:     position.Clone.Rewritten,
				LastError:     position.Clone.LastError,
			}
			if !position.Clone.LastPicked.IsZero() {
				item.Clone.LastPickedAgeMs = now.Sub(position.Clone.LastPicked).Milliseconds()
			}
			if !position.Clone.CreatedAt.IsZero() {
				item.Clone.CreatedAgeMs = now.Sub(position.Clone.CreatedAt).Milliseconds()
			}
		}
		state.Positions = append(state.Positions, item)
	}
	return state
}
