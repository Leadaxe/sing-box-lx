package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/protobuf/types/known/emptypb"
)

// SPEC 073 / 165 — libbox view of a `chain` outbound's state and of the
// config after the lx pre-pass. Objects (not bare strings) cross the gomobile
// bridge — see RunningConfig for why.

// ChainPosition — one position of the chain, in packet order (entry first).
// Now is the node the position resolves to right now; Transparent — a `direct`
// at position >= 1, the dial goes to the hop below; Disabled — the SPEC 075
// runtime toggle (Now stays filled so the UI can show WHAT is disabled).
type ChainPosition struct {
	Tag         string
	IsGroup     bool
	Now         string
	Transparent bool
	Disabled    bool
	Errors      int64
}

type ChainPositionIterator interface {
	Next() *ChainPosition
	HasNext() bool
}

type ChainState struct {
	Tag       string
	Dials     int64
	Errors    int64
	positions []*ChainPosition
}

func (c *ChainState) Positions() ChainPositionIterator {
	return newIterator(c.positions)
}

// GetChainStatus returns the state of one `chain` outbound by tag (SPEC 165:
// unary, like the endpoint status getters).
func (c *CommandClient) GetChainStatus(tag string) (*ChainState, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*ChainState, error) {
		state, err := client.GetChainStatus(ctx, &daemon.GetChainStatusRequest{Tag: tag})
		if err != nil {
			return nil, E.Cause(err, "get chain status")
		}
		return chainStateFromGRPC(state), nil
	})
}

// SetChainPositionEnabled toggles one chain position (packet order, 0 = entry)
// at runtime (SPEC 075). Any combination is valid — disabling every position
// degenerates the chain into direct. The disabled set persists in the
// cache-file and is restored on service start.
func (c *CommandClient) SetChainPositionEnabled(chainTag string, position int32, enabled bool) error {
	_, err := callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*daemon.SetChainPositionEnabledResponse, error) {
		response, err := client.SetChainPositionEnabled(ctx, &daemon.SetChainPositionEnabledRequest{
			ChainTag: chainTag,
			Position: position,
			Enabled:  enabled,
		})
		if err != nil {
			return nil, E.Cause(err, "set chain position enabled")
		}
		return response, nil
	})
	return err
}

// GetEffectiveConfig returns the config the running box was built from after
// the lx pre-pass (SPEC 165): chain hops added, detour of the nodes on chain
// positions rewritten, MTU aligned, lx.detour written out. Same canonical
// form as GetRunningConfig.
func (c *CommandClient) GetEffectiveConfig() (*RunningConfig, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*RunningConfig, error) {
		response, err := client.GetEffectiveConfig(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get effective config")
		}
		return &RunningConfig{content: response.Content}, nil
	})
}

// GetOutboundConfig returns one node as a JSON object {"tag", "original",
// "effective", "changes"} (SPEC 165 §2.2): a chain hop has only effective.
func (c *CommandClient) GetOutboundConfig(tag string) (*RunningConfig, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*RunningConfig, error) {
		response, err := client.GetOutboundConfig(ctx, &daemon.GetOutboundConfigRequest{Tag: tag})
		if err != nil {
			return nil, E.Cause(err, "get outbound config")
		}
		return &RunningConfig{content: response.Content}, nil
	})
}

func chainStateFromGRPC(chain *daemon.ChainState) *ChainState {
	state := &ChainState{Tag: chain.Tag, Dials: chain.Dials, Errors: chain.Errors}
	for _, position := range chain.Positions {
		state.positions = append(state.positions, &ChainPosition{
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
