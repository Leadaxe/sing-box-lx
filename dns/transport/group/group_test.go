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

func newTestGroup(t *testing.T, downTime time.Duration, members ...*fakeMember) *Transport {
	t.Helper()
	tags := make([]string, 0, len(members))
	for _, m := range members {
		tags = append(tags, m.Tag())
	}
	rawTransport, err := NewTransport(context.Background(), testLogger(), "grp", option.GroupDNSServerOptions{
		Servers:  tags,
		DownTime: badoption.Duration(downTime),
	})
	require.NoError(t, err)
	groupTransport := rawTransport.(*Transport)
	for _, m := range members {
		groupTransport.members = append(groupTransport.members, &member{tag: m.Tag(), transport: m})
	}
	return groupTransport
}

func TestFailoverOrderFirstSuccessStops(t *testing.T) {
	first := answering("a")
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(1), first.calls.Load())
	require.Equal(t, int32(0), second.calls.Load())
}

func TestFailoverOnTransportError(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(1), first.calls.Load())
	require.Equal(t, int32(1), second.calls.Load())
}

func TestFailoverOnServfailResponse(t *testing.T) {
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeServerFailure), nil
	})
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(1), second.calls.Load())
}

func TestFailoverOnServfailRcodeError(t *testing.T) {
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return nil, dns.RcodeServerFailure
	})
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(1), second.calls.Load())
}

func TestNXDomainIsNotFailure(t *testing.T) {
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeNameError), nil
	})
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeNameError, response.Rcode)
	require.Equal(t, int32(0), second.calls.Load())
}

func TestNXDomainRcodeErrorIsNotFailure(t *testing.T) {
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return nil, dns.RcodeNameError
	})
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	_, err := group.Exchange(context.Background(), testQuery())
	require.ErrorIs(t, err, dns.RcodeNameError)
	require.Equal(t, int32(0), second.calls.Load())
}

func TestEmptyResponseIsNotFailure(t *testing.T) {
	first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return okResponse(message), nil // NOERROR, no answers
	})
	second := answering("b")
	group := newTestGroup(t, time.Minute, first, second)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Empty(t, response.Answer)
	require.Equal(t, int32(0), second.calls.Load())
}

func TestDownTimeSkipsAndExpires(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, 80*time.Millisecond, first, second)

	// First query: a fails, marked down, b answers.
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(1), first.calls.Load())

	// Within down_time: a is skipped entirely.
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(1), first.calls.Load())
	require.Equal(t, int32(2), second.calls.Load())

	// After expiry: a is polled again.
	time.Sleep(100 * time.Millisecond)
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(2), first.calls.Load())
}

func TestAllDownSingleAttemptRotates(t *testing.T) {
	first := failing("a")
	second := failing("b")
	group := newTestGroup(t, time.Hour, first, second)

	// Both go down.
	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(1), first.calls.Load())
	require.Equal(t, int32(1), second.calls.Load())

	// All down: exactly ONE attempt per query, oldest mark first.
	// a failed before b, so a is retried first; its mark refreshes,
	// so the next query goes to b.
	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(2), first.calls.Load())
	require.Equal(t, int32(1), second.calls.Load())

	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	require.Equal(t, int32(2), first.calls.Load())
	require.Equal(t, int32(2), second.calls.Load())
}

func TestAllDownRecoveryClearsMark(t *testing.T) {
	first := failing("a")
	recovered := atomic.Bool{}
	second := newFakeMember("b", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		if recovered.Load() {
			return okResponse(message), nil
		}
		return nil, E.New("down")
	})
	group := newTestGroup(t, time.Hour, first, second)

	_, err := group.Exchange(context.Background(), testQuery())
	require.Error(t, err)

	recovered.Store(true)
	// Oldest is a (failed first); it still fails and rotates. Next query
	// reaches b, which recovered — success must clear its down mark.
	_, err = group.Exchange(context.Background(), testQuery())
	require.Error(t, err)
	response, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	// b is alive again: served directly, no last-resort path.
	response, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.NotNil(t, response)
}

func TestResetClearsDownState(t *testing.T) {
	first := failing("a")
	second := answering("b")
	group := newTestGroup(t, time.Hour, first, second)
	_, err := group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(1), first.calls.Load())

	group.Reset()
	_, err = group.Exchange(context.Background(), testQuery())
	require.NoError(t, err)
	require.Equal(t, int32(2), first.calls.Load())
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

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a"}, Mode: "bogus"})
	require.ErrorContains(t, err, "unknown mode")

	// interval outside race mode: warning, not an error.
	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{
		Servers:  []string{"a"},
		Interval: badoption.Duration(time.Minute),
	})
	require.NoError(t, err)
}

type fakeTransportOptions struct{}

func registerFakeType(registry *dns.TransportRegistry, transportType string, adapterType string) {
	dns.RegisterTransport[fakeTransportOptions](registry, transportType,
		func(ctx context.Context, logger log.ContextLogger, tag string, options fakeTransportOptions) (adapter.DNSTransport, error) {
			return newFakeMemberWithType(adapterType, tag), nil
		})
}

func newFakeMemberWithType(adapterType string, tag string) *fakeMember {
	return &fakeMember{
		TransportAdapter: dns.NewTransportAdapter(adapterType, tag, nil),
		exchange: func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			return okResponse(message), nil
		},
	}
}

func newTestManager(t *testing.T, defaultTag string) (*dns.TransportManager, *dns.TransportRegistry, context.Context) {
	t.Helper()
	registry := dns.NewTransportRegistry()
	RegisterTransport(registry)
	registerFakeType(registry, "fakeudp", C.DNSTypeUDP)
	registerFakeType(registry, "fakehosts", C.DNSTypeHosts)
	manager := dns.NewTransportManager(testLogger(), registry, nil, defaultTag)
	ctx := service.ContextWith[adapter.DNSTransportManager](context.Background(), manager)
	return manager, registry, ctx
}

func TestManagerRejectsGroupCycle(t *testing.T) {
	manager, _, ctx := newTestManager(t, "g1")
	logger := testLogger()
	require.NoError(t, manager.Create(ctx, logger, "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"g2"}}))
	require.NoError(t, manager.Create(ctx, logger, "g2", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"g1"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "circular server dependency")
}

func TestManagerRejectsMissingMember(t *testing.T) {
	manager, _, ctx := newTestManager(t, "g1")
	require.NoError(t, manager.Create(ctx, testLogger(), "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"missing"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestManagerRejectsLocalSourceMember(t *testing.T) {
	manager, _, ctx := newTestManager(t, "g1")
	logger := testLogger()
	require.NoError(t, manager.Create(ctx, logger, "h", "fakehosts", &fakeTransportOptions{}))
	require.NoError(t, manager.Create(ctx, logger, "g1", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"h"}}))
	err := manager.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not allowed in a group")
}

func TestManagerStartsGroupAndNestedGroup(t *testing.T) {
	manager, _, ctx := newTestManager(t, "outer")
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
