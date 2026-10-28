//go:build with_lx_command

package dns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 — one `dns` stage per server exchange; a cache hit is a REUSED
// stage of its own; the matched DNS rule lands on the enclosing stage.

type answerTransport struct {
	TransportAdapter
	exchanges int
}

func (t *answerTransport) Start(stage adapter.StartStage, _ *adapter.Scope) error { return nil }
func (t *answerTransport) Close() error                                           { return nil }
func (t *answerTransport) Reset()                                                 {}

func (t *answerTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.exchanges++
	response := new(mDNS.Msg)
	response.SetReply(message)
	response.Answer = []mDNS.RR{&mDNS.A{
		Hdr: mDNS.RR_Header{Name: message.Question[0].Name, Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 300},
		A:   net.IPv4(192, 0, 2, 1),
	}}
	return response, nil
}

func (t *answerTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(t.Exchange(ctx, message))
}

func stageAttr(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func TestDNSStage_LX(t *testing.T) {
	client := NewClient(ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().NewLogger("dns")})
	transport := &answerTransport{TransportAdapter: NewTransportAdapter(C.DNSTypeUDP, "remote", nil)}
	trace := lxtrace.New(lxtrace.Options{})
	ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "lookup", "example.org")
	traceDNSRule(ctx, 2, nil)

	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	_, err := client.Exchange(ctx, transport, query.Copy(), adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	_, err = client.Exchange(ctx, transport, query.Copy(), adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, transport.exchanges, "the second query is served from the cache")
	parent.End(nil)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 3)
	require.Equal(t, "final", stageAttr(summary.Stages[0], "dns_rule"))

	exchanged := summary.Stages[1]
	require.Equal(t, "dns", exchanged.Kind)
	require.Equal(t, "remote", exchanged.Subject)
	require.Equal(t, parent.ID(), exchanged.Parent)
	require.Equal(t, lxtrace.OK, exchanged.Result)
	require.Equal(t, C.DNSTypeUDP, stageAttr(exchanged, "type"))
	require.Equal(t, "A example.org", stageAttr(exchanged, "question"))
	require.Equal(t, "NOERROR", stageAttr(exchanged, "rcode"))
	require.Equal(t, "1", stageAttr(exchanged, "answers"))

	cached := summary.Stages[2]
	require.Equal(t, "dns", cached.Kind)
	require.Equal(t, lxtrace.Reused, cached.Result)
	require.Equal(t, "cache", stageAttr(cached, "source"))
	require.NotEmpty(t, stageAttr(cached, "ttl"))
}

func TestDNSStageFailed_LX(t *testing.T) {
	client := NewClient(ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().NewLogger("dns")})
	transport := &staticTransport{NewTransportAdapter(C.DNSTypeUDP, "broken", nil)}
	trace := lxtrace.New(lxtrace.Options{})
	ctx := lxtrace.With(context.Background(), trace)

	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	_, err := client.Exchange(ctx, transport, query, adapter.DNSQueryOptions{}, func(*mDNS.Msg) bool { return true })
	require.Error(t, err)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, lxtrace.Failed, summary.Stages[0].Result)
	require.Equal(t, "SERVFAIL", stageAttr(summary.Stages[0], "rcode"))
	require.Equal(t, summary.Stages[0].ID, summary.RootCause)
}

func TestDNSStageCacheAge_LX(t *testing.T) {
	client := NewClient(ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().NewLogger("dns")})
	transport := &answerTransport{TransportAdapter: NewTransportAdapter(C.DNSTypeUDP, "remote", nil)}
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	// Stored with TTL 300 and 200 s of lifetime left: the entry is 100 s old.
	stored := new(mDNS.Msg)
	stored.SetReply(query)
	stored.Answer = []mDNS.RR{&mDNS.A{
		Hdr: mDNS.RR_Header{Name: "example.org.", Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 300},
		A:   net.IPv4(192, 0, 2, 1),
	}}
	client.storeCache(client.newCacheKey(transport, query.Question[0], query, adapter.DNSQueryOptions{}), stored, 200)

	trace := lxtrace.New(lxtrace.Options{})
	_, err := client.Exchange(lxtrace.With(context.Background(), trace), transport, query, adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	require.Zero(t, transport.exchanges)

	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, lxtrace.Reused, summary.Stages[0].Result)
	require.Contains(t, []string{"100", "101"}, stageAttr(summary.Stages[0], "cache_age"))
}

// stalledTransport answers nothing; the exchange ends with its ctx.
type stalledTransport struct {
	TransportAdapter
}

func (t *stalledTransport) Start(stage adapter.StartStage, _ *adapter.Scope) error { return nil }
func (t *stalledTransport) Close() error                                           { return nil }
func (t *stalledTransport) Reset()                                                 {}

func (t *stalledTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (t *stalledTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(t.Exchange(ctx, message))
}

// A lookup cut short by a cancelled ctx — a DNS group member resolving its
// own server's domain when another member already answered — lost a race
// (§5.13); one that ran out of time stays FAILED (§5.11).
func TestDNSStageCancelledLostRace_LX(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		makeCtx  func(context.Context) (context.Context, context.CancelFunc)
		want     lxtrace.Result
		lostRace bool
	}{
		{"cancelled", func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(ctx)
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, lxtrace.Skipped, true},
		{"timed out", func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 50*time.Millisecond)
		}, lxtrace.Failed, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := NewClient(ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().NewLogger("dns")})
			transport := &stalledTransport{NewTransportAdapter(C.DNSTypeUDP, "stalled", nil)}
			trace := lxtrace.New(lxtrace.Options{})
			ctx, cancel := testCase.makeCtx(lxtrace.With(context.Background(), trace))
			defer cancel()
			query := new(mDNS.Msg)
			query.SetQuestion("example.org.", mDNS.TypeA)
			_, err := client.Exchange(ctx, transport, query, adapter.DNSQueryOptions{}, nil)
			require.Error(t, err)
			summary := trace.Finish(nil)
			require.Len(t, summary.Stages, 1)
			require.Equal(t, testCase.want, summary.Stages[0].Result, "error = %q", summary.Stages[0].Error)
			_, lostRace := lookupAttr(summary.Stages[0], "lost_race")
			require.Equal(t, testCase.lostRace, lostRace)
		})
	}
}

func lookupAttr(stage lxtrace.StageSnapshot, key string) (string, bool) {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return "", false
}
