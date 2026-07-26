package dns

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 035 — emit sites must prefer the effective-server holder (the
// member that actually answered through a group) over the composite
// transport's own tag, and must keep the group tag when the holder is empty
// (cache hits, total failure).

type staticTransport struct {
	TransportAdapter
}

func (t *staticTransport) Start(stage adapter.StartStage) error { return nil }
func (t *staticTransport) Close() error                         { return nil }
func (t *staticTransport) Reset()                               {}

func (t *staticTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	return nil, RcodeServerFailure
}

func (t *staticTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(t.Exchange(ctx, message))
}

func newEmitTestContext(t *testing.T) (context.Context, <-chan dnstrack.QueryEvent, func()) {
	t.Helper()
	manager := dnstrack.NewManager()
	ctx := service.ContextWithPtr(context.Background(), manager)
	subscription, subscriptionDone, err := manager.SubscribeEvents()
	require.NoError(t, err)
	events := make(chan dnstrack.QueryEvent, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case event := <-subscription:
				events <- event
			case <-subscriptionDone:
				// UnSubscribe closes the done channel, not the buffer —
				// a bare range over the subscription never terminates.
				return
			}
		}
	}()
	cleanup := func() {
		manager.UnSubscribeEvents(subscription)
		<-done
	}
	return ctx, events, cleanup
}

func testResponse() *mDNS.Msg {
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	response := new(mDNS.Msg)
	response.SetReply(query)
	return response
}

func TestEmitPrefersEffectiveServer_LX(t *testing.T) {
	ctx, events, cleanup := newEmitTestContext(t)
	defer cleanup()

	groupTransport := &staticTransport{NewTransportAdapter(C.DNSTypeGroup, "grp", nil)}
	ctx = dnstrack.WithQueryTrace(ctx)
	dnstrack.PushGroup(ctx, "grp")
	dnstrack.SetEffectiveServer(ctx, "cloudflare", C.DNSTypeUDP, "proxy-out")
	dnstrack.RecordAttempt(ctx, dnstrack.Attempt{Server: "google", ServerType: C.DNSTypeUDP, Outcome: dnstrack.AttemptTimeout, RTTMs: 5000})
	dnstrack.RecordAttempt(ctx, dnstrack.Attempt{Server: "cloudflare", ServerType: C.DNSTypeUDP, Outcome: dnstrack.AttemptAnswered, RTTMs: 12})
	dnstrack.MarkFanned(ctx)
	dnstrack.MarkSurvival(ctx)

	emitQueryEvent(ctx, groupTransport, testResponse(), dnstrack.SourceExchanged, 60)
	event := <-events
	require.Equal(t, "cloudflare", event.DNSServer)
	require.Equal(t, C.DNSTypeUDP, event.DNSServerType)
	require.Equal(t, []string{"proxy-out"}, event.Outbound)
	require.Equal(t, []string{"grp"}, event.GroupPath)
	require.Len(t, event.Attempts, 2)
	require.Equal(t, dnstrack.AttemptTimeout, event.Attempts[0].Outcome)
	require.True(t, event.Fanned)
	require.True(t, event.Survival)
}

func TestEmitKeepsGroupTagWhenHolderEmpty_LX(t *testing.T) {
	ctx, events, cleanup := newEmitTestContext(t)
	defer cleanup()

	groupTransport := &staticTransport{NewTransportAdapter(C.DNSTypeGroup, "grp", nil)}
	ctx = dnstrack.WithQueryTrace(ctx) // holder present but never touched (cache hit)

	emitQueryEvent(ctx, groupTransport, testResponse(), dnstrack.SourceCached, 60)
	event := <-events
	require.Equal(t, "grp", event.DNSServer)
	require.Equal(t, C.DNSTypeGroup, event.DNSServerType)
	require.Empty(t, event.Outbound)
	require.Empty(t, event.GroupPath)
	require.Empty(t, event.Attempts)
	require.False(t, event.Fanned)
	require.False(t, event.Survival)
}

func TestEmitFailedKeepsGroupTagOnTotalFailure_LX(t *testing.T) {
	ctx, events, cleanup := newEmitTestContext(t)
	defer cleanup()

	groupTransport := &staticTransport{NewTransportAdapter(C.DNSTypeGroup, "grp", nil)}
	ctx = dnstrack.WithQueryTrace(ctx)
	dnstrack.PushGroup(ctx, "grp")
	dnstrack.RecordAttempt(ctx, dnstrack.Attempt{Server: "google", ServerType: C.DNSTypeUDP, Outcome: dnstrack.AttemptTimeout, RTTMs: 10000})

	emitFailedQuery(ctx, groupTransport, mDNS.Question{Name: "example.org.", Qtype: mDNS.TypeA}, dnstrack.RcodeNoAnswer, "all members down")
	event := <-events
	require.True(t, event.Failed)
	require.Equal(t, "grp", event.DNSServer) // effective unset — the group tag stands
	require.Equal(t, []string{"grp"}, event.GroupPath)
	require.Len(t, event.Attempts, 1) // the single last-resort probe
}

func TestEmitDirectTransportUnchanged_LX(t *testing.T) {
	ctx, events, cleanup := newEmitTestContext(t)
	defer cleanup()

	direct := &staticTransport{NewTransportAdapter(C.DNSTypeUDP, "google", nil)}
	emitQueryEvent(ctx, direct, testResponse(), dnstrack.SourceExchanged, 60)
	event := <-events
	require.Equal(t, "google", event.DNSServer)
	require.Equal(t, C.DNSTypeUDP, event.DNSServerType)
}
