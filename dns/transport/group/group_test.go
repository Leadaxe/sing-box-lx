package group

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func testLogger() log.ContextLogger {
	return log.NewNOPFactory().NewLogger("group-test")
}

func testQuery() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("example.org.", mDNS.TypeA)
	return message
}

func okResponse(message *mDNS.Msg) *mDNS.Msg {
	response := new(mDNS.Msg)
	response.SetReply(message)
	return response
}

func rcodeResponse(message *mDNS.Msg, rcode int) *mDNS.Msg {
	response := new(mDNS.Msg)
	response.SetRcode(message, rcode)
	return response
}

type fakeMember struct {
	dns.TransportAdapter
	exchange func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error)
	calls    atomic.Int32
}

func newFakeMember(tag string, exchange func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error)) *fakeMember {
	return &fakeMember{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeUDP, tag, nil),
		exchange:         exchange,
	}
}

func (f *fakeMember) Start(stage adapter.StartStage) error { return nil }
func (f *fakeMember) Close() error                         { return nil }
func (f *fakeMember) Reset()                               {}

func (f *fakeMember) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	f.calls.Add(1)
	return f.exchange(ctx, message)
}

func (f *fakeMember) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(f.Exchange(ctx, message))
}

func answering(tag string) *fakeMember {
	return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return okResponse(message), nil
	})
}

func failing(tag string) *fakeMember {
	return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return nil, E.New("connection refused")
	})
}

// delayed answers successfully after d (or fails with the context's error).
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

// blackhole never answers — it dies only with the context.
func blackhole(tag string) *fakeMember {
	return newFakeMember(tag, func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
}

func newTestGroup(t *testing.T, mode string, errorTTL time.Duration, winTTL time.Duration, members ...*fakeMember) *Transport {
	t.Helper()
	tags := make([]string, 0, len(members))
	for _, m := range members {
		tags = append(tags, m.Tag())
	}
	options := option.GroupDNSServerOptions{
		Servers:  tags,
		Mode:     mode,
		ErrorTTL: badoption.Duration(errorTTL),
	}
	if mode == ModeFastest {
		options.WinTTL = badoption.Duration(winTTL)
	}
	rawTransport, err := NewTransport(context.Background(), testLogger(), "grp", options)
	require.NoError(t, err)
	groupTransport := rawTransport.(*Transport)
	for _, m := range members {
		groupTransport.members = append(groupTransport.members, &member{tag: m.Tag(), transport: m})
	}
	return groupTransport
}

// totalCalls sums exchanges across members.
func totalCalls(members ...*fakeMember) int32 {
	var total int32
	for _, m := range members {
		total += m.calls.Load()
	}
	return total
}

// --- stable ------------------------------------------------------------------

func TestStableStickyOnHealthyNetwork(t *testing.T) {
	first := answering("a")
	second := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)

	for i := 0; i < 10; i++ {
		_, err := group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
	}
	// Sticky: after the first random election every query goes to the same
	// member; the other is never touched.
	require.Equal(t, int32(10), totalCalls(first, second))
	require.True(t, first.calls.Load() == 10 || second.calls.Load() == 10,
		"stable must stick to one member, got a=%d b=%d", first.calls.Load(), second.calls.Load())
}

func TestStableRescueFanReelects(t *testing.T) {
	dying := atomic.Bool{}
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if dying.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	second := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first)
	group.members = append(group.members, &member{tag: "b", transport: second})

	// Establish a as current (only listed member injected first guarantees
	// the initial election can pick either; force by direct state).
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()

	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(1), first.calls.Load())

	// a dies: the query pays a's failure, the rescue fan answers via b,
	// b becomes current.
	dying.Store(true)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	state := group.GroupState()
	require.Equal(t, "b", state.Current)

	// Subsequent queries go to b only (a is dirty).
	aCalls := first.calls.Load()
	for i := 0; i < 3; i++ {
		_, err = group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
	}
	require.Equal(t, aCalls, first.calls.Load())
}

func TestStableNoReturnToRecovered(t *testing.T) {
	dying := atomic.Bool{}
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if dying.Load() {
			return nil, E.New("gone")
		}
		return okResponse(message), nil
	})
	second := answering("b")
	group := newTestGroup(t, ModeStable, 60*time.Millisecond, 0, first)
	group.members = append(group.members, &member{tag: "b", transport: second})
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()

	dying.Store(true)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err) // rescued via b
	dying.Store(false)

	// a's error expires — a is clean again, but stickiness holds b:
	// there is no return-to-primary semantics.
	time.Sleep(80 * time.Millisecond)
	aCalls := first.calls.Load()
	for i := 0; i < 5; i++ {
		_, err = group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
	}
	require.Equal(t, aCalls, first.calls.Load())
	require.Equal(t, "b", group.GroupState().Current)
}

func TestNoCleanAfterFailureMeansNoFan(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)

	// First query: target fails, the other is clean → rescue fan (1+1 calls),
	// which also fails.
	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(2), totalCalls(first, second))

	// Now everybody is dirty: survival — exactly ONE attempt, no fan.
	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(3), totalCalls(first, second))
}

// --- survival ----------------------------------------------------------------

func TestSurvivalRotatesByOldestError(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)

	_, _ = group.Exchange(context.Background(), testQuery()) // poisons target + rescue
	base := totalCalls(first, second)

	// All dirty. Each query: one attempt via the member with the OLDEST
	// last error — alternation emerges because every attempt refreshes
	// the mark.
	var sequence []int32
	for i := 0; i < 4; i++ {
		_, err := group.Exchange(context.Background(), testQuery())
		require.Error(t, err)
		sequence = append(sequence, totalCalls(first, second))
	}
	require.Equal(t, base+4, sequence[3]) // one attempt per query
	require.GreaterOrEqual(t, first.calls.Load(), int32(2))
	require.GreaterOrEqual(t, second.calls.Load(), int32(2))
}

func TestSurvivalSuccessRestoresService(t *testing.T) {
	recovered := atomic.Bool{}
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if recovered.Load() {
			return okResponse(message), nil
		}
		return nil, E.New("down")
	})
	second := failing("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)

	_, _ = group.Exchange(context.Background(), testQuery())
	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err) // full outage

	recovered.Store(true)
	// Survival attempts rotate; within two queries one lands on a,
	// SUCCEEDS, and a's errors are erased — service restored.
	var response *mDNS.Msg
	for i := 0; i < 2 && response == nil; i++ {
		if resp, err := group.Exchange(context.Background(), testQuery()); err == nil {
			response = resp
		}
	}
	require.NotNil(t, response, "survival must find the recovered server within the rotation")

	// a is clean now: the next queries are NORMAL single exchanges to it.
	state := group.GroupState()
	var aState MemberState
	for _, memberState := range state.Members {
		if memberState.Tag == "a" {
			aState = memberState
		}
	}
	require.True(t, aState.Clean)
	bCalls := second.calls.Load()
	for i := 0; i < 3; i++ {
		_, err := group.Exchange(context.Background(), testQuery())
		require.NoError(t, err)
	}
	require.Equal(t, bCalls, second.calls.Load()) // b (dirty) untouched
}

// --- records -----------------------------------------------------------------

func TestErrorTTLExpiryReturnsToCleanSet(t *testing.T) {
	flaky := atomic.Bool{}
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if flaky.Load() {
			return nil, E.New("blip")
		}
		return okResponse(message), nil
	})
	second := answering("b")
	group := newTestGroup(t, ModeStable, 60*time.Millisecond, 0, first)
	group.members = append(group.members, &member{tag: "b", transport: second})
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()

	flaky.Store(true)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err) // rescued by b
	flaky.Store(false)

	require.False(t, memberClean(group, "a"))
	time.Sleep(80 * time.Millisecond)
	require.True(t, memberClean(group, "a"), "error must expire by error_ttl")
}

func memberClean(group *Transport, tag string) bool {
	for _, memberState := range group.GroupState().Members {
		if memberState.Tag == tag {
			return memberState.Clean
		}
	}
	return false
}

func TestServfailIsFailureNxdomainIsAnswer(t *testing.T) {
	servfail := newFakeMember("sf", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeServerFailure), nil
	})
	nx := newFakeMember("nx", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeNameError), nil
	})
	group := newTestGroup(t, ModeStable, time.Hour, 0, servfail)
	group.members = append(group.members, &member{tag: "nx", transport: nx})
	group.access.Lock()
	group.current = "sf"
	group.access.Unlock()

	// SERVFAIL target → error record → rescue fan → NXDOMAIN is a valid
	// answer and wins the fan.
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeNameError, response.Rcode)
	require.False(t, memberClean(group, "sf"))
	require.True(t, memberClean(group, "nx"))
}

func TestResetAmnesty(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, ModeStable, time.Hour, 0, first, second)
	group.access.Lock()
	group.current = "a"
	group.access.Unlock()
	_, _ = group.Exchange(context.Background(), testQuery())
	require.False(t, memberClean(group, "a"))

	group.Reset()
	require.True(t, memberClean(group, "a"))
	require.Empty(t, group.GroupState().Current)
}

// --- validation --------------------------------------------------------------

// Regression for the v1.14.0-lx.22 startup crash: box.preStart starts
// endpoints before DNS transports, so an early dial (WG bind → detour chain
// with a domain node) can reach the group while members is still empty.
// That must be an error, not a panic in the pickers ("invalid argument to IntN").
func TestExchangeBeforeStartReturnsError(t *testing.T) {
	for _, mode := range []string{ModeStable, ModeFastest, ModeParallel} {
		t.Run(mode, func(t *testing.T) {
			rawTransport, err := NewTransport(context.Background(), testLogger(), "grp", option.GroupDNSServerOptions{
				Servers: []string{"a", "b"},
				Mode:    mode,
			})
			require.NoError(t, err)
			group := rawTransport.(*Transport)
			response, err := group.Exchange(context.Background(), testQuery())
			require.Nil(t, response)
			require.ErrorContains(t, err, "not started")
		})
	}
}

func TestConstructorValidation(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	_, err := NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{})
	require.ErrorContains(t, err, "servers is required")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{}})
	require.ErrorContains(t, err, "servers is required")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a", "a"}})
	require.ErrorContains(t, err, "duplicate server")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"g"}})
	require.ErrorContains(t, err, "cannot contain itself")

	// v1 modes are gone — rc.1 configs break loudly, not silently.
	for _, dead := range []string{"failover", "race", "round_robin", "bogus"} {
		_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a"}, Mode: dead})
		require.ErrorContains(t, err, "unknown mode", "mode %q must be rejected", dead)
	}

	// win_ttl outside fastest: warning, not an error.
	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{
		Servers: []string{"a"},
		Mode:    ModeStable,
		WinTTL:  badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
}

// --- manager integration -----------------------------------------------------

type fakeTransportOptions struct{}

func registerFakeType(registry *dns.TransportRegistry, transportType string, adapterType string) {
	dns.RegisterTransport[fakeTransportOptions](registry, transportType,
		func(ctx context.Context, logger log.ContextLogger, tag string, options fakeTransportOptions) (adapter.DNSTransport, error) {
			return &fakeMember{
				TransportAdapter: dns.NewTransportAdapter(adapterType, tag, nil),
				exchange: func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
					return okResponse(message), nil
				},
			}, nil
		})
}

func newTestManager(t *testing.T, defaultTag string) (*dns.TransportManager, context.Context) {
	t.Helper()
	registry := dns.NewTransportRegistry()
	RegisterTransport(registry)
	registerFakeType(registry, "fakeudp", C.DNSTypeUDP)
	registerFakeType(registry, "fakehosts", C.DNSTypeHosts)
	manager := dns.NewTransportManager(testLogger(), registry, nil, defaultTag)
	ctx := service.ContextWith[adapter.DNSTransportManager](context.Background(), manager)
	return manager, ctx
}

func TestManagerRejectsGroupCycle(t *testing.T) {
	manager, ctx := newTestManager(t, "g1")
	logger := testLogger()
	require.NoError(t, manager.Create(ctx, logger, "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"g2"}}))
	require.NoError(t, manager.Create(ctx, logger, "g2", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"g1"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "circular server dependency")
}

func TestManagerRejectsMissingMember(t *testing.T) {
	manager, ctx := newTestManager(t, "g1")
	require.NoError(t, manager.Create(ctx, testLogger(), "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"missing"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestManagerRejectsLocalSourceMember(t *testing.T) {
	manager, ctx := newTestManager(t, "g1")
	logger := testLogger()
	require.NoError(t, manager.Create(ctx, logger, "h", "fakehosts", &fakeTransportOptions{}))
	require.NoError(t, manager.Create(ctx, logger, "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"h"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not allowed in a group")
}

func TestManagerStartsGroupAndNestedGroup(t *testing.T) {
	manager, ctx := newTestManager(t, "outer")
	logger := testLogger()
	require.NoError(t, manager.Create(ctx, logger, "u1", "fakeudp", &fakeTransportOptions{}))
	require.NoError(t, manager.Create(ctx, logger, "u2", "fakeudp", &fakeTransportOptions{}))
	require.NoError(t, manager.Create(ctx, logger, "inner", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"u1", "u2"}}))
	require.NoError(t, manager.Create(ctx, logger, "outer", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"inner", "u1"}}))
	require.NoError(t, manager.Start(adapter.StartStateStart))
	outer, loaded := manager.Transport("outer")
	require.True(t, loaded)
	response, err := outer.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, C.DNSTypeGroup, outer.Type())
}
