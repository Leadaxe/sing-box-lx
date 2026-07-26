package group

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// SPEC 035 v3 — the trace contract table: effective (write-once, leaf under
// nesting), group path (inside-out), attempts, fanned/survival flags.

func TestTraceNormalSingle(t *testing.T) {
	target := answering("a")
	spare := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, target, spare)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.False(t, trace.Fanned)
	require.False(t, trace.Survival)
	require.Len(t, trace.Attempts, 1)
	require.Equal(t, dnstrack.AttemptAnswered, trace.Attempts[0].Outcome)
	require.Equal(t, trace.Attempts[0].Server, trace.EffectiveTag)
	require.Equal(t, []string{"grp"}, trace.GroupPath)
}

func TestTraceRescueFan(t *testing.T) {
	dying := atomic.Bool{}
	target := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if dying.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	rescue := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, target)
	group.members = append(group.members, &member{tag: "b", transport: rescue})
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()

	dying.Store(true)
	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.True(t, trace.Fanned)
	require.False(t, trace.Survival)
	require.Equal(t, "b", trace.EffectiveTag)
	require.Len(t, trace.Attempts, 2)
	require.Equal(t, "a", trace.Attempts[0].Server)
	require.Equal(t, dnstrack.AttemptNetworkError, trace.Attempts[0].Outcome)
	require.Equal(t, "b", trace.Attempts[1].Server)
	require.Equal(t, dnstrack.AttemptAnswered, trace.Attempts[1].Outcome)
}

func TestTraceElectionFanned(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 30*time.Millisecond)
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, slow, fast)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.True(t, trace.Fanned)
	require.Equal(t, "fast", trace.EffectiveTag)
	// Straggler exclusion: the slow member is still running at answer time.
	for _, attempt := range trace.Attempts {
		require.NotEqual(t, "slow", attempt.Server,
			"straggler resolved after the answer must not be in the emitted snapshot")
	}
	waitFanSettled(t, group)
}

func TestTraceSurvival(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)
	_, _ = group.Exchange(context.Background(), testQuery()) // poison both

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.Error(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.True(t, trace.Survival)
	require.False(t, trace.Fanned)
	require.Len(t, trace.Attempts, 1) // one attempt — the whole price
	require.Empty(t, trace.EffectiveTag)
}

func TestTraceSurvivalSuccessAttributed(t *testing.T) {
	alive := answering("alive")
	dead := failing("dead")
	group := newTestGroup(t, ModeStable, time.Hour, 0, dead)
	group.members = append(group.members, &member{tag: "alive", transport: alive})
	// Poison both so survival kicks in, with `alive` carrying the OLDER error.
	group.noteError("alive", 0)
	time.Sleep(2 * time.Millisecond)
	group.noteError("dead", 0)

	ctx := dnstrack.WithQueryTrace(context.Background())
	response, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	require.NotNil(t, response)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.True(t, trace.Survival)
	require.Equal(t, "alive", trace.EffectiveTag)
}

func TestTraceNestedGroupLeafAttribution(t *testing.T) {
	innerRaw, err := NewTransport(context.Background(), testLogger(), "inner", option.GroupDNSServerOptions{
		Servers:  []string{"u1", "u2"},
		ErrorTTL: badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
	inner := innerRaw.(*Transport)
	u1 := failing("u1")
	u2 := answering("u2")
	inner.members = append(inner.members, &member{tag: "u1", transport: u1}, &member{tag: "u2", transport: u2})

	outerRaw, err := NewTransport(context.Background(), testLogger(), "outer", option.GroupDNSServerOptions{
		Servers:  []string{"inner"},
		ErrorTTL: badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
	outer := outerRaw.(*Transport)
	outer.members = append(outer.members, &member{tag: "inner", transport: inner})

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
}

func TestTraceNoHolderNoop(t *testing.T) {
	group := newTestGroup(t, ModeStable, time.Hour, 0, answering("a"))
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	_, ok := dnstrack.SnapshotTrace(context.Background())
	require.False(t, ok)
}

// --- GetDNSGroups snapshot (v3) ---------------------------------------------

func TestGroupStateSnapshotV3(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 8*time.Millisecond)
	broken := failing("broken")
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, fast, slow, broken)

	_, err := group.Exchange(context.Background(), testQuery()) // election
	require.NoError(t, err)
	waitFanSettled(t, group)

	state := group.GroupState()
	require.Equal(t, "grp", state.Tag)
	require.Equal(t, ModeFastest, state.Mode)
	require.Equal(t, "fast", state.Current)

	fastState := memberOf(state, "fast")
	require.True(t, fastState.Clean)
	require.True(t, fastState.Current)
	require.Equal(t, 1, fastState.LiveWins)
	require.Greater(t, fastState.LastRTT, time.Duration(0))

	brokenState := memberOf(state, "broken")
	require.False(t, brokenState.Clean)
	require.Equal(t, 1, brokenState.LiveErrors)
	require.True(t, brokenState.HasError)
	require.GreaterOrEqual(t, brokenState.LastErrorAge, time.Duration(0))
	require.Equal(t, 0, brokenState.LiveWins)
}

func TestGroupStateBeforeFirstQuery(t *testing.T) {
	group := newTestGroup(t, ModeStable, time.Hour, 0, answering("a"))
	state := group.GroupState()
	require.Empty(t, state.Current)
	require.Len(t, state.Members, 1)
	require.True(t, state.Members[0].Clean)
	require.False(t, state.Members[0].Current)
	require.Zero(t, state.Members[0].LiveWins)
	require.Zero(t, state.Members[0].LastRTT)
}
