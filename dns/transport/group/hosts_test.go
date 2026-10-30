package group

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// SPEC 143 — the `hosts` layer of a group: names its hosts members know are
// answered locally before target selection; everything else takes the
// unchanged network path.

// fakeHosts mirrors dns/transport/hosts: PreferredDomain by canonical name,
// Exchange answers A/AAAA of a known name with dns.FixedResponse and
// everything else with NXDOMAIN. exchange overrides the answer when set.
type fakeHosts struct {
	dns.TransportAdapter
	entries  map[string][]netip.Addr
	exchange func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error)
	calls    atomic.Int32
}

func newFakeHosts(tag string, entries map[string][]string) *fakeHosts {
	parsed := make(map[string][]netip.Addr, len(entries))
	for name, addresses := range entries {
		for _, address := range addresses {
			parsed[mDNS.CanonicalName(name)] = append(parsed[mDNS.CanonicalName(name)], netip.MustParseAddr(address))
		}
	}
	return &fakeHosts{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeHosts, tag, nil),
		entries:          parsed,
	}
}

func (f *fakeHosts) Start(stage adapter.StartStage, _ *adapter.Scope) error { return nil }
func (f *fakeHosts) Close() error                                           { return nil }
func (f *fakeHosts) Reset()                                                 {}

func (f *fakeHosts) PreferredDomain(domain string) bool {
	_, loaded := f.entries[domain]
	return loaded
}

func (f *fakeHosts) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	f.calls.Add(1)
	if f.exchange != nil {
		return f.exchange(ctx, message)
	}
	question := message.Question[0]
	if question.Qtype == mDNS.TypeA || question.Qtype == mDNS.TypeAAAA {
		if addresses, loaded := f.entries[mDNS.CanonicalName(question.Name)]; loaded {
			return dns.FixedResponse(message.Id, question, addresses, C.DefaultDNSTTL), nil
		}
	}
	return rcodeResponse(message, mDNS.RcodeNameError), nil
}

func (f *fakeHosts) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	callback(f.Exchange(ctx, message))
}

// staleHosts claims the name but answers otherwise: the race of a hosts file
// re-read between PreferredDomain and Exchange.
func staleHosts(tag string, name string, exchange func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error)) *fakeHosts {
	stale := newFakeHosts(tag, map[string][]string{name: {"192.0.2.1"}})
	stale.exchange = exchange
	return stale
}

func newTestGroupWithHosts(t *testing.T, mode string, errorTTL time.Duration, winTTL time.Duration, hostsMembers []*fakeHosts, members ...*fakeMember) *Transport {
	t.Helper()
	tags := make([]string, 0, len(members))
	for _, m := range members {
		tags = append(tags, m.Tag())
	}
	hostsTags := make([]string, 0, len(hostsMembers))
	for _, h := range hostsMembers {
		hostsTags = append(hostsTags, h.Tag())
	}
	options := option.GroupDNSServerOptions{
		Servers:  tags,
		Hosts:    hostsTags,
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
	for _, h := range hostsMembers {
		groupTransport.hosts = append(groupTransport.hosts, &hostsMember{member: member{tag: h.Tag(), transport: h}, preferred: h})
	}
	return groupTransport
}

func newQuery(name string, qtype uint16) *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion(mDNS.Fqdn(name), qtype)
	return message
}

// answerAddrs returns the A/AAAA addresses of a response.
func answerAddrs(response *mDNS.Msg) []netip.Addr {
	var addresses []netip.Addr
	for _, record := range response.Answer {
		switch answer := record.(type) {
		case *mDNS.A:
			address, _ := netip.AddrFromSlice(answer.A)
			addresses = append(addresses, address.Unmap())
		case *mDNS.AAAA:
			address, _ := netip.AddrFromSlice(answer.AAAA)
			addresses = append(addresses, address)
		}
	}
	return addresses
}

var allModes = []string{ModeStable, ModeFastest, ModeParallel}

func TestHostsHitAnswersLocally(t *testing.T) {
	for _, mode := range allModes {
		t.Run(mode, func(t *testing.T) {
			first := answering("a")
			second := answering("b")
			local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10", "2001:db8::10"}})
			group := newTestGroupWithHosts(t, mode, time.Hour, time.Hour, []*fakeHosts{local}, first, second)

			response, err := group.Exchange(context.Background(), newQuery("wg.home", mDNS.TypeA))
			require.NoError(t, err)
			require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
			require.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.10")}, answerAddrs(response))

			response, err = group.Exchange(context.Background(), newQuery("WG.Home", mDNS.TypeAAAA))
			require.NoError(t, err)
			require.Equal(t, []netip.Addr{netip.MustParseAddr("2001:db8::10")}, answerAddrs(response))

			require.Equal(t, int32(2), local.calls.Load())
			require.Zero(t, totalCalls(first, second), "a hit must not touch the network")
		})
	}
}

func TestHostsOtherFamilyIsEmptyNoError(t *testing.T) {
	network := answering("a")
	local := newFakeHosts("local", map[string][]string{"v4.home": {"203.0.113.11"}})
	group := newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{local}, network)

	response, err := group.Exchange(context.Background(), newQuery("v4.home", mDNS.TypeAAAA))
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Empty(t, response.Answer)
	require.Zero(t, network.calls.Load(), "an IPv4-only name must not send AAAA to the network")
}

func TestHostsKnownNameOtherQtypeGoesToNetwork(t *testing.T) {
	network := answering("a")
	local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
	group := newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{local}, network)

	for _, qtype := range []uint16{mDNS.TypeTXT, mDNS.TypeHTTPS, mDNS.TypeMX} {
		response, err := group.Exchange(context.Background(), newQuery("wg.home", qtype))
		require.NoError(t, err)
		require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	}
	require.Equal(t, int32(3), network.calls.Load())
	require.Zero(t, local.calls.Load(), "a non-address query must not reach hosts")
}

func TestHostsFirstKnowingMemberAnswers(t *testing.T) {
	network := answering("a")
	first := newFakeHosts("first", map[string][]string{"both.home": {"203.0.113.1"}})
	second := newFakeHosts("second", map[string][]string{"both.home": {"203.0.113.2"}, "only.home": {"203.0.113.3"}})
	group := newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{first, second}, network)

	response, err := group.Exchange(context.Background(), newQuery("both.home", mDNS.TypeA))
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.1")}, answerAddrs(response))
	require.Equal(t, int32(1), first.calls.Load())
	require.Zero(t, second.calls.Load(), "the first member in key order answers")

	response, err = group.Exchange(context.Background(), newQuery("only.home", mDNS.TypeA))
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.3")}, answerAddrs(response))
	require.Equal(t, int32(1), first.calls.Load(), "a member that does not know the name is not asked")
	require.Equal(t, int32(1), second.calls.Load())
	require.Zero(t, network.calls.Load())
}

func TestHostsPreferredButNotAnsweredIsMiss(t *testing.T) {
	cases := map[string]func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error){
		"nxdomain": func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			return rcodeResponse(message, mDNS.RcodeNameError), nil
		},
		"servfail": func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			return rcodeResponse(message, mDNS.RcodeServerFailure), nil
		},
		"error": func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
			return nil, E.New("hosts file vanished")
		},
	}
	for name, exchange := range cases {
		t.Run(name, func(t *testing.T) {
			network := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
				return dns.FixedResponse(message.Id, message.Question[0], []netip.Addr{netip.MustParseAddr("198.51.100.7")}, 60), nil
			})
			stale := staleHosts("stale", "wg.home", exchange)
			group := newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{stale}, network)

			ctx := dnstrack.WithQueryTrace(context.Background())
			response, err := group.Exchange(ctx, newQuery("wg.home", mDNS.TypeA))
			require.NoError(t, err)
			require.Equal(t, []netip.Addr{netip.MustParseAddr("198.51.100.7")}, answerAddrs(response))
			require.Equal(t, int32(1), stale.calls.Load())
			require.Equal(t, int32(1), network.calls.Load())

			trace, ok := dnstrack.SnapshotTrace(ctx)
			require.True(t, ok)
			require.Equal(t, "a", trace.EffectiveTag)
			require.Len(t, trace.Attempts, 1)
			require.Equal(t, "a", trace.Attempts[0].Server)
		})
	}
}

// missOutcome is what a miss leaves behind, compared between a group with
// the hosts layer and the same group without it.
type missOutcome struct {
	calls    int32
	failed   bool
	rcode    int
	fanned   bool
	survival bool
}

func TestHostsMissEqualsGroupWithoutHosts(t *testing.T) {
	misses := []*mDNS.Msg{
		newQuery("example.org", mDNS.TypeA),    // unknown to local, stale claims it → NXDOMAIN
		newQuery("wg.home", mDNS.TypeTXT),      // known name, not an address query
		newQuery("unknown.org", mDNS.TypeAAAA), // unknown to everybody
		newQuery("example.org", mDNS.TypeAAAA),
	}
	scenarios := map[string]func(tag string) *fakeMember{
		"healthy":     answering,
		"all_failing": failing, // first query fans (rescue / election / parallel), the rest are survival
	}
	for _, mode := range allModes {
		for name, newMember := range scenarios {
			t.Run(mode+"/"+name, func(t *testing.T) {
				run := func(withHosts bool) ([]missOutcome, int32) {
					first := newMember("a")
					second := newMember("b")
					local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
					stale := staleHosts("stale", "example.org", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
						return rcodeResponse(message, mDNS.RcodeNameError), nil
					})
					var group *Transport
					if withHosts {
						group = newTestGroupWithHosts(t, mode, time.Hour, time.Hour, []*fakeHosts{local, stale}, first, second)
					} else {
						group = newTestGroup(t, mode, time.Hour, time.Hour, first, second)
					}
					var outcomes []missOutcome
					for _, query := range misses {
						ctx := dnstrack.WithQueryTrace(context.Background())
						response, err := group.Exchange(ctx, query.Copy())
						waitFanSettled(t, group)
						trace, ok := dnstrack.SnapshotTrace(ctx)
						require.True(t, ok)
						for _, attempt := range trace.Attempts {
							require.NotEqual(t, C.DNSTypeHosts, attempt.ServerType, "a miss records no hosts probe")
						}
						require.NotEqual(t, "local", trace.EffectiveTag)
						require.NotEqual(t, "stale", trace.EffectiveTag)
						outcome := missOutcome{
							calls:    totalCalls(first, second),
							failed:   err != nil,
							fanned:   trace.Fanned,
							survival: trace.Survival,
						}
						if response != nil {
							outcome.rcode = response.Rcode
						}
						outcomes = append(outcomes, outcome)
					}
					return outcomes, local.calls.Load()
				}
				plain, _ := run(false)
				layered, localCalls := run(true)
				require.Equal(t, plain, layered)
				require.Zero(t, localCalls, "local knows none of the missed address names")
			})
		}
	}
}

func TestHostsMembersHaveNoHealth(t *testing.T) {
	for _, mode := range allModes {
		t.Run(mode, func(t *testing.T) {
			dying := atomic.Bool{}
			first := newFakeMember("a", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
				if dying.Load() {
					return nil, E.New("gone")
				}
				return okResponse(message), nil
			})
			second := answering("b")
			local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
			stale := staleHosts("stale", "example.org", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
				return nil, E.New("hosts file vanished")
			})
			group := newTestGroupWithHosts(t, mode, time.Hour, time.Hour, []*fakeHosts{local, stale}, first, second)

			for i := 0; i < 3; i++ {
				_, err := group.Exchange(context.Background(), newQuery("wg.home", mDNS.TypeA))
				require.NoError(t, err)
				_, err = group.Exchange(context.Background(), newQuery("example.org", mDNS.TypeA))
				require.NoError(t, err)
				waitFanSettled(t, group)
				dying.Store(i == 1)
			}

			group.access.Lock()
			_, localRecord := group.records["local"]
			_, staleRecord := group.records["stale"]
			group.access.Unlock()
			require.False(t, localRecord)
			require.False(t, staleRecord)

			state := group.GroupState()
			var tags []string
			for _, memberState := range state.Members {
				tags = append(tags, memberState.Tag)
			}
			require.Equal(t, []string{"a", "b"}, tags)
			require.NotEqual(t, "local", state.Current)
			require.NotEqual(t, "stale", state.Current)
		})
	}
}

func TestHostsTraceHit(t *testing.T) {
	network := answering("a")
	local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
	group := newTestGroupWithHosts(t, ModeParallel, time.Hour, 0, []*fakeHosts{local}, network)

	ctx := dnstrack.WithQueryTrace(context.Background())
	_, err := group.Exchange(ctx, newQuery("wg.home", mDNS.TypeA))
	require.NoError(t, err)

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.Equal(t, "local", trace.EffectiveTag)
	require.Equal(t, C.DNSTypeHosts, trace.EffectiveType)
	require.Empty(t, trace.EffectiveOutbound)
	require.Equal(t, []string{"grp"}, trace.GroupPath)
	require.False(t, trace.Fanned)
	require.False(t, trace.Survival)
	require.Len(t, trace.Attempts, 1)
	require.Equal(t, "local", trace.Attempts[0].Server)
	require.Equal(t, C.DNSTypeHosts, trace.Attempts[0].ServerType)
	require.Equal(t, dnstrack.AttemptAnswered, trace.Attempts[0].Outcome)
}

func TestHostsTraceMissEqualsGroupWithoutHosts(t *testing.T) {
	snapshot := func(withHosts bool) dnstrack.TraceSnapshot {
		network := answering("a")
		var group *Transport
		if withHosts {
			local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
			stale := staleHosts("stale", "example.org", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
				return rcodeResponse(message, mDNS.RcodeNameError), nil
			})
			group = newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{local, stale}, network)
		} else {
			group = newTestGroup(t, ModeStable, time.Hour, 0, network)
		}
		ctx := dnstrack.WithQueryTrace(context.Background())
		_, err := group.Exchange(ctx, newQuery("example.org", mDNS.TypeA))
		require.NoError(t, err)
		trace, ok := dnstrack.SnapshotTrace(ctx)
		require.True(t, ok)
		for i := range trace.Attempts {
			trace.Attempts[i].RTTMs = 0
		}
		return trace
	}
	require.Equal(t, snapshot(false), snapshot(true))
}

// A nested group with its own hosts layer is an ordinary member for the
// outer group: the layer runs inside the inner Exchange.
func TestHostsNestedGroupLayer(t *testing.T) {
	innerNetwork := answering("u1")
	local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
	inner := newTestGroupWithHosts(t, ModeStable, time.Hour, 0, []*fakeHosts{local}, innerNetwork)
	outerNetwork := answering("u2")
	outer := newTestGroup(t, ModeStable, time.Hour, 0, outerNetwork)
	outer.members = []*member{{tag: "inner", transport: inner}}

	ctx := dnstrack.WithQueryTrace(context.Background())
	response, err := outer.Exchange(ctx, newQuery("wg.home", mDNS.TypeA))
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.10")}, answerAddrs(response))
	require.Zero(t, totalCalls(innerNetwork, outerNetwork))

	trace, ok := dnstrack.SnapshotTrace(ctx)
	require.True(t, ok)
	require.Equal(t, "local", trace.EffectiveTag)
	require.Equal(t, []string{"grp", "grp"}, trace.GroupPath)
}

// --- real hosts transport ----------------------------------------------------

func emptyHostsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	return path
}

func TestHostsRealTransportThroughManager(t *testing.T) {
	manager, ctx := newTestManager(t, "resolver")
	logger := testLogger()
	predefined := new(badjson.TypedMap[string, badoption.Listable[netip.Addr]])
	predefined.Put("wg.home", badoption.Listable[netip.Addr]{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("2001:db8::10")})
	predefined.Put("v4.home", badoption.Listable[netip.Addr]{netip.MustParseAddr("203.0.113.11")})
	require.NoError(t, manager.Create(ctx, logger, "local", C.DNSTypeHosts, &option.HostsDNSServerOptions{
		Path:       []string{emptyHostsFile(t)},
		Predefined: predefined,
	}))
	require.NoError(t, manager.Create(ctx, logger, "net", "fakeudp", &fakeTransportOptions{}))
	require.NoError(t, manager.Create(ctx, logger, "resolver", C.DNSTypeGroup, &option.GroupDNSServerOptions{
		Servers: []string{"net"},
		Hosts:   []string{"local"},
	}))
	require.NoError(t, manager.Start(adapter.StartStateStart, testScope()))

	resolver, loaded := manager.Transport("resolver")
	require.True(t, loaded)
	require.ElementsMatch(t, []string{"net", "local"}, resolver.Dependencies())
	rawNetwork, loaded := manager.Transport("net")
	require.True(t, loaded)
	network := rawNetwork.(*fakeMember)

	response, err := resolver.Exchange(context.Background(), newQuery("wg.home", mDNS.TypeA))
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.10")}, answerAddrs(response))
	require.Equal(t, uint32(C.DefaultDNSTTL), response.Answer[0].Header().Ttl)

	response, err = resolver.Exchange(context.Background(), newQuery("wg.home", mDNS.TypeAAAA))
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("2001:db8::10")}, answerAddrs(response))

	response, err = resolver.Exchange(context.Background(), newQuery("v4.home", mDNS.TypeAAAA))
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Empty(t, response.Answer)
	require.Zero(t, network.calls.Load(), "hits must not reach the network")

	// Misses: an unknown name (real hosts would say NXDOMAIN) and a known
	// name with a non-address type go to the network.
	response, err = resolver.Exchange(context.Background(), newQuery("example.org", mDNS.TypeA))
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(1), network.calls.Load())
	response, err = resolver.Exchange(context.Background(), newQuery("wg.home", mDNS.TypeTXT))
	require.NoError(t, err)
	require.Equal(t, mDNS.RcodeSuccess, response.Rcode)
	require.Equal(t, int32(2), network.calls.Load())
}

// --- validation --------------------------------------------------------------

func TestHostsConstructorValidation(t *testing.T) {
	logger := testLogger()
	ctx := context.Background()

	_, err := NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Hosts: []string{"local"}})
	require.ErrorContains(t, err, "servers is required")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a"}, Hosts: []string{"local", "local"}})
	require.ErrorContains(t, err, "duplicate hosts server: local")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a"}, Hosts: []string{"g"}})
	require.ErrorContains(t, err, "cannot contain itself")

	_, err = NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a", "local"}, Hosts: []string{"local"}})
	require.ErrorContains(t, err, "local is listed in both servers and hosts")

	rawTransport, err := NewTransport(ctx, logger, "g", option.GroupDNSServerOptions{Servers: []string{"a"}, Hosts: []string{"local"}})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "local"}, rawTransport.Dependencies())
}

// "hosts": [] is the same as no key (badjson collapses [] to nil on the
// config path).
func TestHostsEmptyListEqualsNoKey(t *testing.T) {
	registry := dns.NewTransportRegistry()
	RegisterTransport(registry)
	ctx := service.ContextWith[option.DNSTransportOptionsRegistry](context.Background(), registry)

	parse := func(content string) option.GroupDNSServerOptions {
		server, err := json.UnmarshalExtendedContext[option.DNSServerOptions](ctx, []byte(content))
		require.NoError(t, err)
		return *server.Options.(*option.GroupDNSServerOptions)
	}
	noKey := parse(`{"type": "group", "tag": "g", "servers": ["a"]}`)
	emptyKey := parse(`{"type": "group", "tag": "g", "servers": ["a"], "hosts": []}`)
	require.Empty(t, emptyKey.Hosts)
	require.Equal(t, noKey, emptyKey)

	rawTransport, err := NewTransport(context.Background(), testLogger(), "g", emptyKey)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, rawTransport.Dependencies())
	require.Empty(t, rawTransport.(*Transport).hostsTags)
}

func TestManagerValidatesHostsMembers(t *testing.T) {
	cases := []struct {
		name  string
		hosts string
		err   string
	}{
		{"missing", "missing", "not found"},
		{"udp", "u2", "only servers of type hosts, got udp: u2"},
		{"fakeip", "f", "only servers of type hosts, got fakeip: f"},
		{"group", "inner", "only servers of type hosts, got group: inner"},
		{"no_preferred_domain", "h", "does not report the names it knows: h"},
		{"real_hosts", "local", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manager, ctx := newTestManager(t, "g")
			logger := testLogger()
			require.NoError(t, manager.Create(ctx, logger, "u1", "fakeudp", &fakeTransportOptions{}))
			require.NoError(t, manager.Create(ctx, logger, "u2", "fakeudp", &fakeTransportOptions{}))
			require.NoError(t, manager.Create(ctx, logger, "f", "fakefakeip", &fakeTransportOptions{}))
			require.NoError(t, manager.Create(ctx, logger, "h", "fakehosts", &fakeTransportOptions{}))
			require.NoError(t, manager.Create(ctx, logger, "local", C.DNSTypeHosts, &option.HostsDNSServerOptions{Path: []string{emptyHostsFile(t)}}))
			require.NoError(t, manager.Create(ctx, logger, "inner", C.DNSTypeGroup, &option.GroupDNSServerOptions{Servers: []string{"u2"}}))
			require.NoError(t, manager.Create(ctx, logger, "g", C.DNSTypeGroup, &option.GroupDNSServerOptions{
				Servers: []string{"u1"},
				Hosts:   []string{testCase.hosts},
			}))
			err := manager.Start(adapter.StartStateStart, testScope())
			if testCase.err == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.err)
		})
	}
}
