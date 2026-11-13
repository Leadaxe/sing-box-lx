package lxsniffguard

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// pendingOnRelay leaves cover a pending pair on the suspect relay key: a
// silent failure with protection, which the descent probe allows once.
func pendingOnRelay(t *testing.T, g *Guard, clock *fakeClock) {
	t.Helper()
	decisions, err := dial(g.Node("cover"), ProtoTLS, pathRelay, deadSilent)
	require.Equal(t, []bool{true}, decisions)
	require.Error(t, err)
	clock.Advance(time.Second)
}

// A handshake that starts after Node.Dial settled its attempt (a lazy
// handshake, early data) goes by the label: no mark, and the pending pair
// stays for the next dial.
func TestDecideAfterSettle(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	pendingOnRelay(t, g, clock)

	var late *Attempt
	require.NoError(t, node.Dial(context.Background(), func(ctx context.Context) error {
		late = AttemptFromContext(ctx)
		return nil
	}))
	decision := late.Decide(ProtoTLS, pathRelay, true)
	require.True(t, decision.Protect, "by the suspect label, not by the pair")
	require.Nil(t, late.mark)

	decisions, err := dial(node, ProtoTLS, pathRelay, alive)
	require.NoError(t, err)
	require.Equal(t, []bool{false}, decisions, "the pair is still there for the next dial")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay))
}

// Withdraw drops the mark: the attempt is neither counted nor retried, the
// pair it took goes to the next dial, and a later Decide sets no mark.
func TestWithdraw(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	pendingOnRelay(t, g, clock)

	calls := 0
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		calls++
		attempt := AttemptFromContext(ctx)
		require.False(t, attempt.Decide(ProtoTLS, pathRelay, true).Protect, "the pair's opposite")
		attempt.Withdraw()
		require.True(t, attempt.Decide(ProtoTLS, pathRelay, true).Protect, "by the label once withdrawn")
		return errRefused
	})
	require.ErrorIs(t, err, errRefused)
	require.Equal(t, 1, calls, "a withdrawn attempt is not retried")

	decisions, err := dial(node, ProtoTLS, pathRelay, alive)
	require.NoError(t, err)
	require.Equal(t, []bool{false}, decisions, "the pair was released")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay))

	var nilAttempt *Attempt
	nilAttempt.Withdraw()
}

// Begin is the attempt of a seam whose dial runs under its own lifetime
// context: Decide takes the pending pair, Late hands the node over, Report
// completes the pair.
func TestBeginReport(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	connect := func(ok func(protect bool) bool) bool {
		attempt := node.Begin()
		protect := attempt.Decide(ProtoQUIC, pathDirect, true).Protect
		attempt.Late().Report(ProtoQUIC, pathDirect, protect, ok(protect))
		return protect
	}
	sniffed := func(protect bool) bool { return protect }
	require.False(t, connect(sniffed))
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoQUIC, pathDirect))
	clock.Advance(time.Second)
	require.True(t, connect(sniffed), "the next connection takes the pending pair")
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoQUIC, pathDirect))

	// A Begin attempt is never settled by a dial: withdrawn, it reports
	// nothing and leaves the label alone.
	attempt := node.Begin()
	attempt.Decide(ProtoQUIC, pathDirect, true)
	attempt.Withdraw()
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoQUIC, pathDirect))

	var nilNode *Node
	require.Nil(t, nilNode.Begin())
}

func TestAttemptMethodAndContext(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	attempt := g.Node("cover").Begin()
	require.Equal(t, option.LXSniffGuardQUICDecoy, attempt.Method(ProtoQUIC))
	require.Equal(t, option.LXSniffGuardTLSRecordFragment, attempt.Method(ProtoTLS))
	require.Equal(t, "", attempt.Method(0))
	var nilAttempt *Attempt
	require.Equal(t, "", nilAttempt.Method(ProtoQUIC))

	ctx := context.Background()
	require.Equal(t, ctx, ContextWithAttempt(ctx, nil))
	require.Same(t, attempt, AttemptFromContext(ContextWithAttempt(ctx, attempt)))
}
