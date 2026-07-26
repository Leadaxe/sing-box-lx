package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

// SPEC 035/036: the group must record the answering member (write-once — the
// leaf wins under nesting), the group path (inside-out), and the probe
// chronology into the client-provided trace holder; without a holder it all
// must be a no-op.

func TestTraceFailoverChain(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.Equal(t, "b", trace.EffectiveTag)
	require.NotEmpty(t, trace.EffectiveType)
	require.Equal(t, []string{"grp"}, trace.GroupPath)
	require.Len(t, trace.Attempts, 2)
	require.Equal(t, "a", trace.Attempts[0].Server)
	require.Equal(t, dnstrack.AttemptNetworkError, trace.Attempts[0].Outcome)
	require.Equal(t, "b", trace.Attempts[1].Server)
	require.Equal(t, dnstrack.AttemptAnswered, trace.Attempts[1].Outcome)
	require.False(t, trace.Racer)
}

func TestTraceTotalFailure(t *testing.T) {
	group := newTestGroup(t, time.Minute, failing("a"), failing("b"))
	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.Error(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.Empty(t, trace.EffectiveTag) // nobody answered — that IS the state
	require.Len(t, trace.Attempts, 2)
}

func TestTraceLastResortSingleAttempt(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, time.Hour, first, second)
	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err)

	// All down: the next query makes exactly ONE last-resort attempt, and
	// its trace carries exactly that probe.
	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err = group.Exchange(ctx, testQuery())
	require.Error(t, err)
	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.Len(t, trace.Attempts, 1)
	require.Equal(t, dnstrack.AttemptNetworkError, trace.Attempts[0].Outcome)
}

func TestTraceRaceWinnerSnapshotExcludesStraggler(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 60*time.Millisecond)
	broken := failing("broken")
	group := newTestRaceGroup(t, time.Hour, time.Hour, slow, fast, broken)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)

	// Snapshot right after the answer — the emit moment. The slow straggler
	// is still running: it must NOT be in the trace. The broken member may
	// or may not have resolved yet; the winner must be there.
	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.True(t, trace.Racer)
	require.Equal(t, "fast", trace.EffectiveTag)
	var servers []string
	for _, attempt := range trace.Attempts {
		servers = append(servers, attempt.Server)
	}
	require.Contains(t, servers, "fast")
	require.NotContains(t, servers, "slow")

	// The straggler finishes in the background and lands in the HOLDER —
	// harmless: the emitted snapshot above did not change.
	waitRanking(t, group, 2)
	late, _ := dnstrack.SnapshotTrace(ctx)
	require.GreaterOrEqual(t, len(late.Attempts), len(trace.Attempts))
	require.NotContains(t, servers, "slow")
}

func TestTraceBetweenRacesSingleWinnerAttempt(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 10*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, fast, slow)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err = group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.False(t, trace.Racer)
	require.Len(t, trace.Attempts, 1)
	require.Equal(t, "fast", trace.Attempts[0].Server)
	require.Equal(t, dnstrack.AttemptAnswered, trace.Attempts[0].Outcome)
}

func TestTraceNestedGroupLeafAttribution(t *testing.T) {
	innerRaw, err := NewTransport(context.Background(), testLogger(), "inner", option.GroupDNSServerOptions{
		Servers:  []string{"u1", "u2"},
		DownTime: badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
	inner := innerRaw.(*Transport)
	u1 := failing("u1")
	u2 := answering("u2")
	inner.members = append(inner.members, &member{tag: "u1", transport: u1}, &member{tag: "u2", transport: u2})

	outerRaw, err := NewTransport(context.Background(), testLogger(), "outer", option.GroupDNSServerOptions{
		Servers:  []string{"inner", "x"},
		DownTime: badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
	outer := outerRaw.(*Transport)
	outer.members = append(outer.members, &member{tag: "inner", transport: inner}, &member{tag: "x", transport: answering("x")})

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err = outer.Exchange(ctx, testQuery())
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	// write-once: the LEAF that answered, not the inner group's tag.
	require.Equal(t, "u2", trace.EffectiveTag)
	// inside-out path.
	require.Equal(t, []string{"inner", "outer"}, trace.GroupPath)
	// leaves only: no attempt row typed "group".
	for _, attempt := range trace.Attempts {
		require.NotEqual(t, "inner", attempt.Server)
		require.NotEqual(t, "group", attempt.ServerType)
	}
	require.Len(t, trace.Attempts, 2) // u1 failed, u2 answered
}

func TestTraceNoHolderNoop(t *testing.T) {
	group := newTestGroup(t, time.Minute, answering("a"))
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	_, ok := dnstrack.SnapshotTrace(context.Background())
	require.False(t, ok)
}

func TestGroupStateSnapshot(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 10*time.Millisecond)
	broken := failing("broken")
	group := newTestRaceGroup(t, time.Hour, time.Hour, fast, slow, broken)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)

	state := group.GroupState()
	require.Equal(t, "grp", state.Tag)
	require.Equal(t, ModeRace, state.Mode)
	require.Equal(t, "fast", state.Winner)
	require.Equal(t, []string{"fast", "slow"}, state.Ranking)
	require.True(t, state.HasRaced)
	require.GreaterOrEqual(t, state.RaceAge, time.Duration(0))
	require.Len(t, state.Members, 3)

	byTag := make(map[string]MemberState)
	for _, memberSnapshot := range state.Members {
		byTag[memberSnapshot.Tag] = memberSnapshot
	}
	require.True(t, byTag["fast"].Up)
	require.Greater(t, byTag["fast"].LastRTT, time.Duration(0))
	require.Zero(t, byTag["fast"].ConsecutiveFailures)
	require.False(t, byTag["broken"].Up)
	require.Greater(t, byTag["broken"].DownRemaining, time.Duration(0))
	require.Equal(t, 1, byTag["broken"].ConsecutiveFailures)
}

func TestGroupStateBeforeFirstRace(t *testing.T) {
	group := newTestRaceGroup(t, time.Hour, time.Hour, answering("a"))
	state := group.GroupState()
	require.False(t, state.HasRaced)
	require.Empty(t, state.Winner)
	require.Empty(t, state.Ranking)
	require.Len(t, state.Members, 1)
	require.True(t, state.Members[0].Up)
	require.Zero(t, state.Members[0].LastRTT)
}
