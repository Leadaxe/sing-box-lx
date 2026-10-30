//go:build with_lx_command

package group

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 §5.13 — a parallel group answered by one member cancels the
// request's ctx, cutting the others short. A DoT member caught in its TLS
// handshake lost the race: its dns_member stage is SKIPPED with
// lost_race=<the winner's member stage>, its `tls` stage SKIPPED with
// lost_race, not FAILED (field run on LxBox v2.25.18-rc.3: two FAILED tls under a group step
// that ended OK).
func TestParallelLoserHandshakeLostRace_LX(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	helloSent := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// The ClientHello is in: the member is inside its handshake. Never answer.
		_, _ = conn.Read(make([]byte, 1))
		close(helloSent)
		_, _ = io.Copy(io.Discard, conn)
	}()
	tlsConfig, err := tls.NewClient(context.Background(), testLogger(), "dot.example", option.OutboundTLSOptions{Enabled: true, ServerName: "dot.example"})
	require.NoError(t, err)
	dot := transport.NewTLSRaw(testLogger(), dns.NewTransportAdapter(C.DNSTypeTLS, "dot", nil), N.SystemDialer, M.SocksaddrFromNet(listener.Addr()), tlsConfig)
	defer dot.Reset()

	group := newTestGroup(t, ModeParallel, time.Minute, 0, held("fast", helloSent))
	group.members = append(group.members, &member{tag: "dot", transport: dot})

	client := dns.NewClient(dns.ClientOptions{Context: context.Background(), Logger: testLogger()})
	trace := lxtrace.New(lxtrace.Options{})
	ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "app_dns", "example.org")
	_, err = client.Exchange(ctx, group, testQuery(), adapter.DNSQueryOptions{}, nil)
	require.NoError(t, err)
	parent.End(nil)

	// The loser's stages end after the answer, on the cancelled ctx.
	var members []lxtrace.StageSnapshot
	var handshake lxtrace.StageSnapshot
	require.Eventually(t, func() bool {
		members = memberStagesLx(trace, parent)
		if len(members) != 2 || members[0].Result == lxtrace.Running || members[1].Result == lxtrace.Running {
			return false
		}
		for _, member := range members {
			for _, child := range trace.Stage(member.ID).Children() {
				if child.Kind == "tls" && child.Result != lxtrace.Running {
					handshake = child
					return true
				}
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	summary := trace.Finish(nil)
	require.Zero(t, summary.RootCause)

	winner, loser := memberByTagLx(t, members, "fast"), memberByTagLx(t, members, "dot")
	require.Equal(t, lxtrace.OK, winner.Result)
	require.Equal(t, lxtrace.Skipped, loser.Result, "attrs = %v, error = %q", loser.Attrs, loser.Error)
	require.Equal(t, strconv.FormatUint(uint64(winner.ID), 10), attrLx(loser, "lost_race"), "the loser names the winner")
	require.Equal(t, C.DNSTypeTLS, attrLx(loser, "type"))

	require.Equal(t, loser.ID, handshake.Parent, "the handshake sits under its member")
	require.Equal(t, lxtrace.Skipped, handshake.Result, "attrs = %v, error = %q", handshake.Attrs, handshake.Error)
	require.Equal(t, "0", attrLx(handshake, "lost_race"))
}

// A member that fails on its own before the winner answers is FAILED: that is
// a real failure, not a lost race.
func TestParallelOwnFailureStaysFailed_LX(t *testing.T) {
	release := make(chan struct{})
	broken := newFakeMember("broken", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		defer close(release)
		return nil, E.New("connection refused")
	})
	group := newTestGroup(t, ModeParallel, time.Minute, 0, broken, held("slow", release))
	trace := lxtrace.New(lxtrace.Options{})
	ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "app_dns", "example.org")
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	parent.End(nil)
	var members []lxtrace.StageSnapshot
	require.Eventually(t, func() bool {
		members = memberStagesLx(trace, parent)
		return len(members) == 2 && members[0].Result != lxtrace.Running && members[1].Result != lxtrace.Running
	}, 2*time.Second, 10*time.Millisecond)
	trace.Finish(nil)
	require.Equal(t, lxtrace.Failed, memberByTagLx(t, members, "broken").Result)
	require.Equal(t, lxtrace.OK, memberByTagLx(t, members, "slow").Result)
}

// The single path labels its member too.
func TestSingleMemberStage_LX(t *testing.T) {
	group := newTestGroup(t, ModeFastest, time.Minute, time.Minute, answering("a"))
	trace := lxtrace.New(lxtrace.Options{})
	ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "app_dns", "example.org")
	_, err := group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	_, err = group.Exchange(ctx, testQuery())
	require.NoError(t, err)
	parent.End(nil)
	trace.Finish(nil)
	members := memberStagesLx(trace, parent)
	require.Len(t, members, 2)
	for _, member := range members {
		require.Equal(t, "a", member.Subject)
		require.Equal(t, lxtrace.OK, member.Result)
		require.Equal(t, C.DNSTypeUDP, attrLx(member, "type"))
	}
}

// SPEC 143: a hosts hit is one dns_member stage of the hosts member that
// answered; a miss leaves no stage of a hosts member, only the network one's.
func TestHostsHitMemberStage_LX(t *testing.T) {
	network := answering("a")
	local := newFakeHosts("local", map[string][]string{"wg.home": {"203.0.113.10"}})
	stale := staleHosts("stale", "example.org", func(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
		return rcodeResponse(message, mDNS.RcodeNameError), nil
	})
	group := newTestGroupWithHosts(t, ModeStable, time.Minute, 0, []*fakeHosts{local, stale}, network)

	stagesOf := func(query *mDNS.Msg) []lxtrace.StageSnapshot {
		trace := lxtrace.New(lxtrace.Options{})
		ctx, parent := lxtrace.Begin(lxtrace.With(context.Background(), trace), "app_dns", query.Question[0].Name)
		_, err := group.Exchange(ctx, query)
		require.NoError(t, err)
		parent.End(nil)
		trace.Finish(nil)
		return memberStagesLx(trace, parent)
	}

	hit := stagesOf(newQuery("wg.home", mDNS.TypeA))
	require.Len(t, hit, 1)
	require.Equal(t, "local", hit[0].Subject)
	require.Equal(t, lxtrace.OK, hit[0].Result)
	require.Equal(t, C.DNSTypeHosts, attrLx(hit[0], "type"))
	require.Empty(t, attrLx(hit[0], "outbound"))

	misses := []*mDNS.Msg{
		newQuery("example.org", mDNS.TypeA), // stale claims it, answers NXDOMAIN
		newQuery("unknown.org", mDNS.TypeA),
		newQuery("wg.home", mDNS.TypeTXT),
	}
	for _, query := range misses {
		miss := stagesOf(query)
		require.Len(t, miss, 1, "query %s", query.Question[0].String())
		require.Equal(t, "a", miss[0].Subject)
		require.Equal(t, C.DNSTypeUDP, attrLx(miss[0], "type"))
	}
	require.Equal(t, int32(1), stale.calls.Load())
}

// memberStagesLx returns the dns_member stages under parent, at any depth.
func memberStagesLx(trace *lxtrace.Trace, parent *lxtrace.Stage) []lxtrace.StageSnapshot {
	var members []lxtrace.StageSnapshot
	var walk func(children []lxtrace.StageSnapshot)
	walk = func(children []lxtrace.StageSnapshot) {
		for _, child := range children {
			if child.Kind == "dns_member" {
				members = append(members, child)
			}
			walk(trace.Stage(child.ID).Children())
		}
	}
	walk(parent.Children())
	return members
}

func memberByTagLx(t *testing.T, members []lxtrace.StageSnapshot, tag string) lxtrace.StageSnapshot {
	t.Helper()
	for _, member := range members {
		if member.Subject == tag {
			return member
		}
	}
	t.Fatalf("no dns_member %s in %v", tag, members)
	return lxtrace.StageSnapshot{}
}

func attrLx(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}
