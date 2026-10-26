//go:build !with_lx_command

package lxtrace

import (
	"context"
	"errors"
	"testing"

	"github.com/sagernet/sing-box/adapter"
)

// Without with_lx_command the package is inert: every call returns at once
// and nothing is recorded, so the `// lx:` insertions cost nothing.
func TestStubIsInert(t *testing.T) {
	trace := New(Options{Fresh: true, Probe: true, Depth: DepthRoute, Emit: func(StageSnapshot) {
		t.Fatal("stub must never emit")
	}})
	ctx := With(context.Background(), trace)
	if From(ctx) != nil || Current(ctx) != nil {
		t.Fatal("stub must not carry a trace")
	}
	if Fresh(ctx) || IsProbe(ctx) || DepthOf(ctx) != DepthHTTP {
		t.Fatal("stub accessors must be zero")
	}
	childCtx, s := Begin(ctx, "dial", "x")
	if s != nil || childCtx != ctx {
		t.Fatal("stub Begin must return (ctx, nil)")
	}
	lookupCtx, ls, err := BeginLookup(ctx, "r", "d")
	if ls != nil || err != nil || lookupCtx != ctx {
		t.Fatal("stub BeginLookup must return (ctx, nil, nil)")
	}
	s.Attr("k", "v")
	s.End(errors.New("x"))
	s.Mark(Reused)
	s.LostRace(nil)
	if s.ID() != 0 {
		t.Fatal("stub stage id must be 0")
	}
	if Routed(ctx, &adapter.InboundContext{}, nil, nil) {
		t.Fatal("stub Routed must return false")
	}
	summary := trace.Finish(errors.New("x"))
	if len(summary.Stages) != 0 || summary.RootCause != 0 || summary.Truncated {
		t.Fatal("stub summary must be empty")
	}
	if ErrResolveCycle == nil || DefaultMaxStages != 512 {
		t.Fatal("stub must export the same constants")
	}
}
