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
// context: Decide takes the pending pair, Report completes the pair.
func TestBeginReport(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	connect := func(ok func(protect bool) bool) bool {
		attempt := node.Begin()
		protect := attempt.Decide(ProtoQUIC, pathDirect, true).Protect
		attempt.Report(ok(protect))
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
	attempt.Report(false)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoQUIC, pathDirect))
	g.access.Lock()
	require.Nil(t, node.pending, "a withdrawn attempt reports nothing")
	g.access.Unlock()

	var nilNode *Node
	require.Nil(t, nilNode.Begin())
	var nilAttempt *Attempt
	nilAttempt.Report(false)
}

// Report counts the outcome once, against the mark Decide set; an attempt
// that never decided, and one Node.Dial settles, report nothing.
func TestReportOnceAndOnlyBegin(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	node := g.Node("cover")
	pending := func() *pendingPair {
		g.access.Lock()
		defer g.access.Unlock()
		return node.pending
	}

	node.Begin().Report(false)
	require.Nil(t, pending(), "no mark: nothing to report")

	attempt := node.Begin()
	require.False(t, attempt.Decide(ProtoQUIC, pathDirect, true).Protect)
	attempt.Report(true)
	attempt.Report(false)
	require.Nil(t, pending(), "the second report is dropped")

	var dialled *Attempt
	require.NoError(t, node.Dial(context.Background(), func(ctx context.Context) error {
		dialled = AttemptFromContext(ctx)
		dialled.Decide(ProtoQUIC, pathDirect, true)
		return nil
	}))
	dialled.Report(false)
	require.Nil(t, pending(), "Node.Dial already counted the attempt")
}

// The network changes between Decide and the late report: the outcome
// belongs to the old network and leaves the fresh label of the direct path
// alone.
func TestReportAfterNetworkChange(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")

	failed := node.Begin()
	require.False(t, failed.Decide(ProtoQUIC, pathDirect, true).Protect)
	failed.Report(false)
	clock.Advance(time.Second)
	paired := node.Begin()
	require.True(t, paired.Decide(ProtoQUIC, pathDirect, true).Protect, "takes the pending pair")
	g.NetworkChanged()
	paired.Report(true)
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoQUIC, pathDirect), "no pair completes across a network change")

	stale := node.Begin()
	require.False(t, stale.Decide(ProtoQUIC, pathDirect, true).Protect)
	g.NetworkChanged()
	stale.Report(false)
	g.access.Lock()
	require.Nil(t, node.pending, "a failure from the old network leaves no pair")
	g.access.Unlock()
}

// A late success of an attempt that began after a pending pair appeared
// closes that pair, as any other outcome of a marked attempt does.
func TestReportSuccessClosesOlderPair(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	pendingOnRelay(t, g, clock)

	attempt := node.Begin()
	require.False(t, attempt.Decide(ProtoQUIC, pathDirect, true).Protect)
	g.access.Lock()
	require.NotNil(t, node.pending)
	g.access.Unlock()
	attempt.Report(true)
	g.access.Lock()
	require.Nil(t, node.pending, "the success closed the older pair")
	g.access.Unlock()
}

// A handshake on another path inside a marked attempt (a nested dial that
// still carries it) neither takes over the mark nor gets a decision; the
// first handshake keeps its mark and the pair it took.
func TestDecideOtherKeyKeepsMark(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	pendingOnRelay(t, g, clock)

	calls := 0
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		calls++
		attempt := AttemptFromContext(ctx)
		first := attempt.Decide(ProtoTLS, pathRelay, true)
		require.False(t, first.Protect, "the pair's opposite")
		require.Equal(t, Decision{}, attempt.Decide(ProtoTLS, pathDirect, true), "another key does not intervene")
		require.Equal(t, Decision{}, attempt.Decide(ProtoQUIC, pathRelay, true))
		require.Equal(t, first, attempt.Decide(ProtoTLS, pathRelay, true), "the same key keeps its decision")
		g.access.Lock()
		require.Equal(t, labelKey{tag: "relay", proto: ProtoTLS}, attempt.mark.key)
		require.NotNil(t, attempt.mark.pair)
		require.True(t, attempt.mark.pair.taken)
		g.access.Unlock()
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay), "the pair completed on the first key")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))
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
