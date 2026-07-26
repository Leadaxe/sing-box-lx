package group

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func newTestRaceGroup(t *testing.T, interval time.Duration, downTime time.Duration, members ...*fakeMember) *Transport {
	t.Helper()
	tags := make([]string, 0, len(members))
	for _, m := range members {
		tags = append(tags, m.Tag())
	}
	rawTransport, err := NewTransport(context.Background(), testLogger(), "grp", option.GroupDNSServerOptions{
		Servers:  tags,
		Mode:     ModeRace,
		Interval: badoption.Duration(interval),
		DownTime: badoption.Duration(downTime),
	})
	require.NoError(t, err)
	groupTransport := rawTransport.(*Transport)
	for _, m := range members {
		groupTransport.members = append(groupTransport.members, &member{tag: m.Tag(), transport: m})
	}
	return groupTransport
}

// delayed answers successfully after d.
func delayed(tag string, d time.Duration) *fakeMember {
	return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		select {
		case <-time.After(d):
			return okResponse(message), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
}

func waitRanking(t *testing.T, group *Transport, size int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		group.access.Lock()
		ranking := append([]string(nil), group.race.ranking...)
		running := group.race.running
		group.access.Unlock()
		if len(ranking) >= size && !running {
			return ranking
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ranking did not reach %d entries", size)
	return nil
}

func TestRaceFirstQueryPicksFastest(t *testing.T) {
	fast := delayed("fast", 5*time.Millisecond)
	slow := delayed("slow", 60*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, slow, fast) // list order ≠ speed order

	start := time.Now()
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	// The racer answers on the FIRST success — it must not wait for slow.
	require.Less(t, time.Since(start), 50*time.Millisecond)

	ranking := waitRanking(t, group, 2)
	require.Equal(t, []string{"fast", "slow"}, ranking)

	// Within interval: only the winner is queried.
	fastCalls, slowCalls := fast.calls.Load(), slow.calls.Load()
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, fastCalls+1, fast.calls.Load())
	require.Equal(t, slowCalls, slow.calls.Load())
}

func TestRaceRerunsAfterInterval(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	slow := delayed("slow", 10*time.Millisecond)
	group := newTestRaceGroup(t, 60*time.Millisecond, time.Hour, fast, slow)

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)

	time.Sleep(80 * time.Millisecond)
	slowCalls := slow.calls.Load()
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)
	// A new race fanned out: slow was queried again.
	require.Greater(t, slow.calls.Load(), slowCalls)
}

func TestRaceFailedMemberGoesDownAndSkipsNextRace(t *testing.T) {
	fast := delayed("fast", time.Millisecond)
	broken := failing("broken")
	group := newTestRaceGroup(t, 30*time.Millisecond, time.Hour, fast, broken)

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 1)
	brokenCalls := broken.calls.Load()

	time.Sleep(50 * time.Millisecond)
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 1)
	// broken is inside down_time: the second race must not include it.
	require.Equal(t, brokenCalls, broken.calls.Load())
}

func TestRaceWinnerFailureFallsToRanking(t *testing.T) {
	failNow := atomic.Bool{}
	first := newFakeMember("first", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if failNow.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	second := delayed("second", 20*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, first, second)

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, waitRanking(t, group, 2))

	failNow.Store(true)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.GreaterOrEqual(t, second.calls.Load(), int32(2))
}

func TestRaceNXDomainWins(t *testing.T) {
	nx := newFakeMember("nx", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeNameError), nil
	})
	slow := delayed("slow", 50*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, nx, slow)

	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeNameError, response.Rcode)

	group.access.Lock()
	winner := group.race.winner
	group.access.Unlock()
	require.Equal(t, "nx", winner)
}

func TestRaceAllFailedErrorsAndNoImmediateRerace(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestRaceGroup(t, time.Hour, time.Hour, first, second)

	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err)

	// Wait for the race to fully settle (running=false).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		group.access.Lock()
		running := group.race.running
		group.access.Unlock()
		if !running {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Next query: no new race (interval far away, everyone down) —
	// exactly ONE last-resort attempt, oldest mark first.
	aCalls, bCalls := first.calls.Load(), second.calls.Load()
	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(1), first.calls.Load()+second.calls.Load()-aCalls-bCalls)
}

func TestRaceConcurrentCandidatesSingleRace(t *testing.T) {
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
	group := newTestRaceGroup(t, time.Hour, time.Hour, first, second)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = group.Exchange(context.Background(), testQuery())
		}(i)
	}
	time.Sleep(30 * time.Millisecond) // both queries are in flight, race gated
	close(release)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	waitRanking(t, group, 2)
	// One race fan-out (1 call per member) + at most one winner-routed
	// query from the non-racer.
	total := first.calls.Load() + second.calls.Load()
	require.LessOrEqual(t, total, int32(3))
	require.GreaterOrEqual(t, total, int32(2))
}

func TestRaceNoBackgroundActivityWhenIdle(t *testing.T) {
	first := answering("a")
	second := answering("b")
	group := newTestRaceGroup(t, 10*time.Millisecond, time.Hour, first, second)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)
	callsAfterRace := first.calls.Load() + second.calls.Load()

	// interval expires many times over — with no queries there must be
	// no probes (lazy semantics, ENERGY invariant).
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, callsAfterRace, first.calls.Load()+second.calls.Load())
}

func TestRaceResetForcesNewRace(t *testing.T) {
	first := delayed("a", time.Millisecond)
	second := delayed("b", 5*time.Millisecond)
	group := newTestRaceGroup(t, time.Hour, time.Hour, first, second)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	waitRanking(t, group, 2)

	group.Reset()
	secondCalls := second.calls.Load()
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	// Reset dropped the winner: the next query raced again (b queried).
	waitRanking(t, group, 2)
	require.Greater(t, second.calls.Load(), secondCalls)
}
