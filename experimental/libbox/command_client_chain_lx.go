package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/protobuf/types/known/emptypb"
)

// SPEC 073 — libbox view of a `chain` outbound's state. Objects (not bare strings)
// cross the gomobile bridge — see RunningConfig for why.

// ChainCloneState — the live link instance of a node at a position >= 1.
// State: starting | active | idle. Ages are milliseconds; -1 = unknown.
type ChainCloneState struct {
	State           string
	ActiveConns     int64
	LastPickedAgeMs int64
	CreatedAgeMs    int64
	MTUConfigured   int32
	MTUEffective    int32
	MTUReason       string
	stripped        []string
	Rewritten       bool
	LastError       string
}

// Stripped — what `strip` removed from this link, as an iterator.
func (c *ChainCloneState) Stripped() StringIterator {
	return newIterator(c.stripped)
}

// ChainPosition — one position of the chain, in packet order (entry first).
// Now is the node the position resolves to right now; Transparent — a `direct`
// at position >= 1 that collapses the hop; Disabled — the SPEC 075 runtime
// toggle (Now stays filled so the UI can show WHAT is disabled). Clone is nil
// for position 0, for transparent and disabled positions and while no link
// exists yet.
type ChainPosition struct {
	Tag         string
	IsGroup     bool
	Now         string
	Transparent bool
	Disabled    bool
	Errors      int64
	Clone       *ChainCloneState
}

type ChainPositionIterator interface {
	Next() *ChainPosition
	HasNext() bool
}

type ChainState struct {
	Tag           string
	Dials         int64
	Errors        int64
	ClonesCreated int64
	ClonesEvicted int64
	LiveClones    int64
	positions     []*ChainPosition
}

func (c *ChainState) Positions() ChainPositionIterator {
	return newIterator(c.positions)
}

type ChainStateIterator interface {
	Next() *ChainState
	HasNext() bool
}

// GetChains returns the state of every `chain` outbound in the running config.
func (c *CommandClient) GetChains() (ChainStateIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (ChainStateIterator, error) {
		list, err := client.GetChains(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get chains")
		}
		return chainListFromGRPC(list), nil
	})
}

// ChainToggleResult — SPEC 075. An object (not a bare string) crosses the
// gomobile bridge — see RunningConfig for why. WarmupError is empty when the
// link warmed up fine or warm-up was not applicable (urltest position,
// direct/block leaf, disable); the toggle itself HAS been applied either way.
type ChainToggleResult struct {
	WarmupError string
}

// SetChainPositionEnabled toggles one chain position (packet order, 0 = entry)
// at runtime (SPEC 075). Any combination is valid — disabling every position
// degenerates the chain into direct. The disabled set persists in the
// cache-file and is restored on service start.
func (c *CommandClient) SetChainPositionEnabled(chainTag string, position int32, enabled bool) (*ChainToggleResult, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*ChainToggleResult, error) {
		response, err := client.SetChainPositionEnabled(ctx, &daemon.SetChainPositionEnabledRequest{
			ChainTag: chainTag,
			Position: position,
			Enabled:  enabled,
		})
		if err != nil {
			return nil, E.Cause(err, "set chain position enabled")
		}
		return &ChainToggleResult{WarmupError: response.WarmupError}, nil
	})
}

// GetChainCloneConfig returns the effective post-transform options JSON
// ({type, tag, ...} after strip/rewrite/MTU/detour) of the live link at the
// position's currently resolved leaf (SPEC 075). Errors with NotFound when no
// live link exists (position 0, transparent, evicted, disabled).
func (c *CommandClient) GetChainCloneConfig(chainTag string, position int32) (*RunningConfig, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*RunningConfig, error) {
		response, err := client.GetChainCloneConfig(ctx, &daemon.GetChainCloneConfigRequest{
			ChainTag: chainTag,
			Position: position,
		})
		if err != nil {
			return nil, E.Cause(err, "get chain clone config")
		}
		return &RunningConfig{content: response.Content}, nil
	})
}

func chainListFromGRPC(list *daemon.ChainList) ChainStateIterator {
	if list == nil || len(list.Chains) == 0 {
		return newIterator([]*ChainState{})
	}
	var states []*ChainState
	for _, chain := range list.Chains {
		state := &ChainState{
			Tag:           chain.Tag,
			Dials:         chain.Dials,
			Errors:        chain.Errors,
			ClonesCreated: chain.ClonesCreated,
			ClonesEvicted: chain.ClonesEvicted,
			LiveClones:    chain.LiveClones,
		}
		for _, position := range chain.Positions {
			item := &ChainPosition{
				Tag:         position.Tag,
				IsGroup:     position.IsGroup,
				Now:         position.Now,
				Transparent: position.Transparent,
				Disabled:    position.Disabled,
				Errors:      position.Errors,
			}
			if position.Clone != nil {
				item.Clone = &ChainCloneState{
					State:           position.Clone.State,
					ActiveConns:     position.Clone.ActiveConns,
					LastPickedAgeMs: position.Clone.LastPickedAgeMs,
					CreatedAgeMs:    position.Clone.CreatedAgeMs,
					MTUConfigured:   int32(position.Clone.MtuConfigured),
					MTUEffective:    int32(position.Clone.MtuEffective),
					MTUReason:       position.Clone.MtuReason,
					stripped:        position.Clone.Stripped,
					Rewritten:       position.Clone.Rewritten,
					LastError:       position.Clone.LastError,
				}
			}
			state.positions = append(state.positions, item)
		}
		states = append(states, state)
	}
	return newIterator(states)
}
