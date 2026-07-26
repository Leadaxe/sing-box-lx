package group

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// --- fastest -----------------------------------------------------------------

func TestFastestColdStartElection(t *testing.T) {
	fast := delayed("fast", 2*time.Millisecond)
	slow := delayed("slow", 40*time.Millisecond)
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, slow, fast)

	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)

	state := group.GroupState()
	require.Equal(t, "fast", state.Current)
	require.Equal(t, 1, memberOf(state, "fast").LiveWins)

	// Between elections: single exchanges to the winner only.
	waitFanSettled(t, group)
	slowCalls := slow.calls.Load()
	for i := 0; i < 3; i++ {
		_, err = group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
	}
	require.Equal(t, slowCalls, slow.calls.Load())
}

func memberOf(state State, tag string) MemberState {
	for _, memberState := range state.Members {
		if memberState.Tag == tag {
			return memberState
		}
	}
	return MemberState{}
}

// waitFanSettled waits until no fan stragglers are in flight (election flag
// released and, generously, a settling delay for collector goroutines).
func waitFanSettled(t *testing.T, group *Transport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		group.access.Lock()
		running := group.election
		group.access.Unlock()
		if !running {
			time.Sleep(60 * time.Millisecond) // let stragglers finish
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fan did not settle")
}

func TestFastestElectionSingleFlight(t *testing.T) {
	release := make(chan struct{})
	gated := func(tag string) *fakeMember {
		return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			select {
			case <-release:
				return okResponse(message), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}
	first := gated("a")
	second := gated("b")
	third := gated("c")
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, first, second, third)

	const burst = 5
	var wg sync.WaitGroup
	errs := make([]error, burst)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = group.Exchange(context.Background(), testQuery())
		}(i)
	}
	time.Sleep(30 * time.Millisecond) // whole burst in flight, all gated
	close(release)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	// Single-flight: ONE election fan (3 calls) + each concurrent goes to
	// one random clean member (burst-1 calls). Without the flag this would
	// be burst×3.
	total := totalCalls(first, second, third)
	require.LessOrEqual(t, total, int32(3+burst-1))
	require.GreaterOrEqual(t, total, int32(3))
}

func TestFastestWinExpiryTriggersReElection(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 8*time.Millisecond)
	group := newTestGroup(t, ModeFastest, time.Hour, 70*time.Millisecond, fast, slow)

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitFanSettled(t, group)

	// Wins expire → «нет данных» → next query fans again.
	time.Sleep(90 * time.Millisecond)
	slowCalls := slow.calls.Load()
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitFanSettled(t, group)
	require.Greater(t, slow.calls.Load(), slowCalls, "expired wins must cause a new election fan")
}

func TestFastestWinnerErrorErasesWinsAndReelects(t *testing.T) {
	dying := atomic.Bool{}
	fast := newFakeMember("fast", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if dying.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	backup := delayed("backup", 5*time.Millisecond)
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, fast, backup)

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitFanSettled(t, group)
	require.Equal(t, "fast", group.GroupState().Current)

	// The winner dies: the query pays its failure, the rescue fan answers
	// via backup; the winner's wins are ERASED with the error record —
	// no flap-back when its error expires before the win would have.
	dying.Store(true)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.NotNil(t, response)
	state := group.GroupState()
	require.Equal(t, 0, memberOf(state, "fast").LiveWins, "error must erase live wins")
	require.Equal(t, "backup", state.Current)
	require.Equal(t, 1, memberOf(state, "backup").LiveWins, "rescue fan win is competitive")
}

// --- parallel ----------------------------------------------------------------

func TestParallelFansEveryQueryNoWins(t *testing.T) {
	first := delayed("a", time.Millisecond)
	second := delayed("b", 10*time.Millisecond)
	group := newTestGroup(t, ModeParallel, time.Hour, 0, first, second)

	for i := 0; i < 3; i++ {
		_, err := group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
		time.Sleep(20 * time.Millisecond) // let stragglers finish
	}
	require.Equal(t, int32(3), first.calls.Load())
	require.Equal(t, int32(3), second.calls.Load())
	state := group.GroupState()
	require.Empty(t, state.Current, "parallel has no sticky target")
	require.Equal(t, 0, memberOf(state, "a").LiveWins, "parallel must not record wins")
}

func TestParallelObeysSurvivalGuard(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, ModeParallel, time.Hour, 0, first, second)

	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err) // fan, both fail → both dirty
	base := totalCalls(first, second)

	// No clean members: parallel obeys the anti-storm frame too — one
	// attempt, no fan.
	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, base+1, totalCalls(first, second))
}

// --- budget & guard ----------------------------------------------------------

func TestTargetBudgetLeavesRoomForRescue(t *testing.T) {
	hole := blackhole("hole")
	rescue := answering("rescue")
	group := newTestGroup(t, ModeStable, time.Hour, 0, hole)
	group.members = append(group.members, &member{tag: "rescue", transport: rescue})
	group.access.Lock()
	group.current = "hole"
	group.access.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	response, err := group.Exchange(ctx, testQuery())
	elapsed := time.Since(started)

	// The blackholed target burns only HALF the budget; the rescue fan runs
	// in the guaranteed remainder and saves the query.
	require.NoError(t, err, "rescue fan must have budget after a blackholed target")
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Less(t, elapsed, 400*time.Millisecond)
	require.GreaterOrEqual(t, elapsed, 150*time.Millisecond) // ~half spent on the target
	require.False(t, memberClean(group, "hole"))
	require.True(t, memberClean(group, "rescue"))
}

func TestDeadBudgetDoesNotPoisonFanMembers(t *testing.T) {
	hole := blackhole("hole")
	slowRescue := blackhole("slow1") // never answers either
	slowRescue2 := blackhole("slow2")
	group := newTestGroup(t, ModeStable, time.Hour, 0, hole)
	group.members = append(group.members,
		&member{tag: "slow1", transport: slowRescue},
		&member{tag: "slow2", transport: slowRescue2})
	group.access.Lock()
	group.current = "hole"
	group.access.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, err := group.Exchange(ctx, testQuery())
	require.Error(t, err)
	waitFanSettled(t, group)

	// The target's failure is real (its sub-deadline was honest) — recorded.
	// The fan members died WITH the request budget: recording their errors
	// would poison the whole group because of one blackholed target.
	require.False(t, memberClean(group, "hole"))
	require.True(t, memberClean(group, "slow1"), "fan failure after budget death must not be recorded")
	require.True(t, memberClean(group, "slow2"), "fan failure after budget death must not be recorded")
}

func TestFanLateSuccessHealsButDoesNotAnswer(t *testing.T) {
	dying := atomic.Bool{}
	target := newFakeMember("target", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if dying.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	quick := delayed("quick", 2*time.Millisecond)
	late := delayed("late", 50*time.Millisecond)
	group := newTestGroup(t, ModeStable, time.Hour, 0, target)
	group.members = append(group.members,
		&member{tag: "quick", transport: quick},
		&member{tag: "late", transport: late})
	group.access.Lock()
	group.current = "target"
	group.access.Unlock()

	dying.Store(true)
	started := time.Now()
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.NotNil(t, response)
	// The answer came from the quick rescuer, without waiting for the late one.
	require.Less(t, time.Since(started), 40*time.Millisecond)
	require.Equal(t, "quick", group.GroupState().Current)

	// The late success still heals its server (erases errors — nothing to
	// erase here) and does not change the current.
	waitFanSettled(t, group)
	require.Equal(t, "quick", group.GroupState().Current)
}
