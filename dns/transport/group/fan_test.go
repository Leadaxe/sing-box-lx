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

// --- review regression tests (SPEC 033 v2 audit) -----------------------------

func TestResetMidFanDoesNotPoisonFreshTables(t *testing.T) {
	release := make(chan struct{})
	gatedFail := func(tag string) *fakeMember {
		return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			select {
			case <-release:
				return nil, E.New("late failure")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}
	first := gatedFail("a")
	second := gatedFail("b")
	group := newTestGroup(t, ModeParallel, time.Hour, 0, first, second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = group.Exchange(context.Background(), testQuery())
	}()
	time.Sleep(20 * time.Millisecond) // fan in flight
	group.Reset()                     // network-change amnesty
	close(release)                    // members now fail with a LIVE ctx
	<-done
	waitFanSettled(t, group)

	// The stale fan's failures must be dropped by the gen guard: the
	// amnestied tables stay clean.
	require.True(t, memberClean(group, "a"), "stale fan failure must not poison amnestied tables")
	require.True(t, memberClean(group, "b"), "stale fan failure must not poison amnestied tables")
}

func TestResetMidSingleExchangeDoesNotPoison(t *testing.T) {
	release := make(chan struct{})
	target := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		select {
		case <-release:
			return nil, E.New("late failure")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	spare := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, target)
	group.members = append(group.members, &member{tag: "b", transport: spare})
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = group.Exchange(context.Background(), testQuery())
	}()
	time.Sleep(20 * time.Millisecond)
	group.Reset()
	close(release)
	<-done
	waitFanSettled(t, group)

	require.True(t, memberClean(group, "a"), "stale single-exchange failure must not poison amnestied tables")
}

func TestCollectFanNeverYieldsNilNil(t *testing.T) {
	// All fan failures abandoned (dead ctx): the guard skips every error
	// record, errs stays empty — the fan must still resolve to a NON-nil
	// error, never (nil, nil).
	hole1 := blackhole("h1")
	hole2 := blackhole("h2")
	group := newTestGroup(t, ModeParallel, time.Hour, 0, hole1, hole2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // context dead before the fan even starts
	response, err := group.fan(ctx, testQuery(), []*member{
		{tag: "h1", transport: hole1},
		{tag: "h2", transport: hole2},
	}, 0, false)
	require.Error(t, err, "a fan must never resolve to (nil, nil)")
	require.Nil(t, response)
	waitFanSettled(t, group)
	require.True(t, memberClean(group, "h1"))
	require.True(t, memberClean(group, "h2"))
}

func TestNegativeWinTTLNormalized(t *testing.T) {
	group := newTestGroup(t, ModeFastest, time.Hour, -time.Second, answering("a"))
	require.Equal(t, DefaultWinTTL, group.winTTL, "negative win_ttl must fall back to the default")
}

func TestElectionConcurrentDoesNotTrashCurrent(t *testing.T) {
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
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, first, second)

	const burst = 6
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = group.Exchange(context.Background(), testQuery())
		}()
	}
	time.Sleep(30 * time.Millisecond)
	// While the election is gated, concurrents serve via random clean
	// members — current must remain UNSET (only the fan winner elects it).
	group.access.Lock()
	currentDuringElection := group.current
	group.access.Unlock()
	require.Empty(t, currentDuringElection, "election-window concurrents must not elect current")

	close(release)
	wg.Wait()
	waitFanSettled(t, group)
	require.NotEmpty(t, group.GroupState().Current, "the fan winner must elect current")
}

func TestPostDeathFanSuccessMintsNoWin(t *testing.T) {
	slow := delayed("slow", 60*time.Millisecond)
	group := newTestGroup(t, ModeFastest, time.Hour, time.Hour, slow)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := group.fan(ctx, testQuery(), []*member{{tag: "slow", transport: slow}}, 0, false)
	require.Error(t, err) // ctx dies before the answer

	// The success lands after the context ended: it belongs to a failed
	// query — no win, no current.
	time.Sleep(60 * time.Millisecond)
	state := group.GroupState()
	require.Equal(t, 0, memberOf(state, "slow").LiveWins, "post-death success must not mint a win")
	require.Empty(t, state.Current)
}
