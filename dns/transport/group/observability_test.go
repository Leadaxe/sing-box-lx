package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dnstrack"

	"github.com/stretchr/testify/require"
)

// SPEC 035: the group must record the ACTUAL answering member into the
// client-provided context holder; without a holder it must be a no-op.

func TestEffectiveServerFailover(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)

	ctx := dnstrack.WithEffectiveServer(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	tag, serverType, _, isSet := dnstrack.EffectiveServerFromContext(ctx)
	require.True(t, isSet)
	require.Equal(t, "b", tag)
	require.NotEmpty(t, serverType)
}

func TestEffectiveServerTotalFailureUnset(t *testing.T) {
	group := newTestGroup(t, time.Minute, failing("a"), failing("b"))
	ctx := dnstrack.WithEffectiveServer(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.Error(t, err)
	_, _, _, isSet := dnstrack.EffectiveServerFromContext(ctx)
	require.False(t, isSet)
}

func TestEffectiveServerRaceWinner(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 40*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, slow, fast)

	ctx := dnstrack.WithEffectiveServer(context.Background())
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	tag, _, _, isSet := dnstrack.EffectiveServerFromContext(ctx)
	require.True(t, isSet)
	require.Equal(t, "fast", tag)

	// Between races: the winner-routed query attributes to the winner too.
	waitRanking(t, group, 2)
	ctx = dnstrack.WithEffectiveServer(context.Background())
	_, err = group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	tag, _, _, isSet = dnstrack.EffectiveServerFromContext(ctx)
	require.True(t, isSet)
	require.Equal(t, "fast", tag)
}

func TestEffectiveServerNoHolderNoop(t *testing.T) {
	group := newTestGroup(t, time.Minute, answering("a"))
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	_, _, _, isSet := dnstrack.EffectiveServerFromContext(context.Background())
	require.False(t, isSet)
}
