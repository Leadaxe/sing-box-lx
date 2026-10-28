//go:build with_lx_command

package dns

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 §5.6 — fresh=true bypasses the DNS cache at every level: a
// nested lookup (the caller's options, not the test's) neither reads nor
// writes it. Without fresh, and without a trace, the cache works as before.

func freshTestClient() (*Client, *answerTransport) {
	client := NewClient(ClientOptions{Context: context.Background(), Logger: log.NewNOPFactory().NewLogger("dns")})
	transport := &answerTransport{TransportAdapter: NewTransportAdapter(C.DNSTypeUDP, "remote", nil)}
	return client, transport
}

var freshLookupOptions = adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only}

func TestFreshLookupBypassesCacheRead_LX(t *testing.T) {
	client, transport := freshTestClient()
	_, err := client.Lookup(context.Background(), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	require.Equal(t, 1, transport.exchanges)

	trace := lxtrace.New(lxtrace.Options{Fresh: true, Probe: true})
	_, err = client.Lookup(lxtrace.With(context.Background(), trace), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	require.Equal(t, 2, transport.exchanges, "fresh asks the server despite a cached answer")
	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, lxtrace.OK, summary.Stages[0].Result)
	require.Empty(t, stageAttr(summary.Stages[0], "source"), "no REUSED cache stage")

	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	trace = lxtrace.New(lxtrace.Options{Fresh: true, Probe: true})
	_, err = client.Exchange(lxtrace.With(context.Background(), trace), transport, query, adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, transport.exchanges, "the Exchange path bypasses the cache too")
	trace.Finish(nil)
}

func TestFreshLookupBypassesCacheWrite_LX(t *testing.T) {
	client, transport := freshTestClient()
	trace := lxtrace.New(lxtrace.Options{Fresh: true, Probe: true})
	ctx := lxtrace.With(context.Background(), trace)
	_, err := client.Lookup(ctx, transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	query := new(mDNS.Msg)
	query.SetQuestion("example.org.", mDNS.TypeA)
	_, err = client.Exchange(ctx, transport, query, adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	trace.Finish(nil)
	require.Equal(t, 2, transport.exchanges)

	_, err = client.Lookup(context.Background(), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	require.Equal(t, 3, transport.exchanges, "the test's answers were not cached")
}

func TestWarmLookupUsesCache_LX(t *testing.T) {
	client, transport := freshTestClient()
	_, err := client.Lookup(context.Background(), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	_, err = client.Lookup(context.Background(), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	require.Equal(t, 1, transport.exchanges, "no trace: the cache answers")

	trace := lxtrace.New(lxtrace.Options{Probe: true})
	_, err = client.Lookup(lxtrace.With(context.Background(), trace), transport, "example.org", freshLookupOptions, nil)
	require.NoError(t, err)
	require.Equal(t, 1, transport.exchanges, "fresh=false: the cache answers")
	summary := trace.Finish(nil)
	require.Len(t, summary.Stages, 1)
	require.Equal(t, lxtrace.Reused, summary.Stages[0].Result)
}

func TestTraceFreshOptionsNoTrace_LX(t *testing.T) {
	options := traceFreshOptions(context.Background(), adapter.DNSQueryOptions{})
	require.False(t, options.DisableCache)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		traceFreshOptions(context.Background(), adapter.DNSQueryOptions{})
	}))
}
