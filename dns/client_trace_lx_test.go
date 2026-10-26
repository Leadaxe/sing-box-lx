//go:build with_lx_command

package dns

import (
	"context"
	"net"
	"testing"

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
