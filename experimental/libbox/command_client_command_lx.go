package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/protobuf/types/known/emptypb"
)

// DnsQuery is the libbox view of one DNS resolution (SPEC 018). Failed marks
// timeout/SERVFAIL/rejected; Rcode is -1 when there was no response. Answers is the full
// response (CNAME hops + A/AAAA) in order, present only when SubscribeDNSQueries was asked
// to include it. ProcessInfo carries the same app attribution as a Connection.
type DnsQuery struct {
	Domain        string
	QueryType     int32
	Rcode         int32
	TTL           int32
	Source        string
	Failed        bool
	Error         string
	DNSServer     string // which DNS server (transport) resolved this (SPEC 018); for a group — the answering member (SPEC 035)
	DNSServerType string // udp / tls / https / quic
	Fanned        bool   // the query involved a group fan-out: rescue / election / parallel (SPEC 035)
	Survival      bool   // answered via the least dirty server — no clean members (SPEC 035)
	ProcessInfo   *ProcessInfo
	answers       []*DnsAnswer
	outbound      []string
	groupPath     []string
	attempts      []*DnsGroupAttempt
}

// Answers returns the resolution's resource records (CNAME chain + final addresses) in
// wire order. Empty unless includeAnswers was set on the subscription.
func (q *DnsQuery) Answers() DnsAnswerIterator {
	return newIterator(q.answers)
}

// Outbound returns the channel the query went out through: the DNS server's detour, with a
// selector expanded to its live node. Empty on cached/optimistic (the query never left).
func (q *DnsQuery) Outbound() StringIterator {
	return newIterator(q.outbound)
}

// GroupPath returns the DNS-group nesting the query went through, inside-out (SPEC 035).
// Empty = the query did not go through a group (or was served from the group's cache).
func (q *DnsQuery) GroupPath() StringIterator {
	return newIterator(q.groupPath)
}

// Attempts returns the probe chronology of the query through its group(s), snapshotted at
// answer time — race stragglers that resolved later are absent by design; the completed
// picture is GetDNSGroups. Empty for non-group queries and cache hits. SPEC 035.
func (q *DnsQuery) Attempts() DnsGroupAttemptIterator {
	return newIterator(q.attempts)
}

// DnsGroupAttempt is one resolved member probe of a DNS group (SPEC 035). Outcome is one
// of: "answered" (valid response, NXDOMAIN/empty included), "timeout", "network_error",
// "servfail". RTTMs is the probe duration in milliseconds.
type DnsGroupAttempt struct {
	Server     string
	ServerType string
	Outcome    string
	RTTMs      int32
}

type DnsGroupAttemptIterator interface {
	Next() *DnsGroupAttempt
	HasNext() bool
}

// DnsAnswer is one resource record. Type is dns.Type; RData is the textual record (CNAME
// target or IP). Kept distinct from the final IP so a client can rebuild the CNAME chain.
type DnsAnswer struct {
	Name  string
	Type  int32
	RData string
	TTL   int32
}

type DnsAnswerIterator interface {
	Next() *DnsAnswer
	HasNext() bool
}

// handleDNSStream multiplexes the SPEC 018 DNS-query stream into the CommandClient exactly
// like handleConnectionsStream: it runs on the shared c.ctx (which Connect() recreates on
// reconnect), so the DNS stream auto-recovers with every other profiler stream and dies with
// the client — no standalone subscription, no per-stream Close(), no bespoke reconnect. A
// recv error routes through Disconnected() (the common path that drives the client's
// reconnect), not a DNS-specific OnError. includeAnswers comes from options, like
// StatusInterval. Dispatched from dispatchCommands on CommandDNS.
func (c *CommandClient) handleDNSStream() {
	client, ctx := c.getStreamContext()

	stream, err := client.SubscribeDNSQueries(ctx, &daemon.SubscribeDNSQueriesRequest{
		IncludeAnswers: c.options.DNSIncludeAnswers,
	})
	if err != nil {
		c.handler.Disconnected(E.Cause(err, "subscribe dns queries").Error())
		return
	}

	for {
		event, err := stream.Recv()
		if err != nil {
			c.handler.Disconnected(E.Cause(err, "dns query stream recv").Error())
			return
		}
		c.handler.WriteDNSQuery(dnsQueryFromGRPC(event))
	}
}

func dnsQueryFromGRPC(event *daemon.DnsQueryEvent) *DnsQuery {
	query := &DnsQuery{
		Domain:        event.Domain,
		QueryType:     int32(event.QueryType),
		Rcode:         event.Rcode,
		TTL:           int32(event.Ttl),
		Source:        event.Source,
		Failed:        event.Failed,
		Error:         event.Error,
		DNSServer:     event.DnsServer,
		DNSServerType: event.DnsServerType,
		Fanned:        event.Fanned,
		Survival:      event.Survival,
		outbound:      event.Outbound,
		groupPath:     event.DnsGroupPath,
	}
	for _, attempt := range event.Attempts {
		query.attempts = append(query.attempts, &DnsGroupAttempt{
			Server:     attempt.Server,
			ServerType: attempt.ServerType,
			Outcome:    attempt.Outcome,
			RTTMs:      int32(attempt.RttMs),
		})
	}
	if event.ProcessInfo != nil {
		query.ProcessInfo = &ProcessInfo{
			ProcessID:    int64(event.ProcessInfo.ProcessId),
			UserID:       event.ProcessInfo.UserId,
			UserName:     event.ProcessInfo.UserName,
			ProcessPath:  event.ProcessInfo.ProcessPath,
			packageNames: event.ProcessInfo.PackageNames,
		}
	}
	for _, answer := range event.Answers {
		query.answers = append(query.answers, &DnsAnswer{
			Name:  answer.Name,
			Type:  int32(answer.Type),
			RData: answer.Rdata,
			TTL:   int32(answer.Ttl),
		})
	}
	return query
}

// SPEC 014 — client side of the libbox command-protocol extensions (CONSTITUTION §3.6).
// The generated gRPC stubs (client.URLTestOutbound / client.GetRules) always exist after
// proto regeneration, so this wrapper is not behind a build-tag: the method is present in
// every libbox.aar and LxBox can call it. Behaviour is decided by the core build — a core
// without with_lx_command answers codes.Unimplemented (see started_service_command_lx_stub.go).

// URLTestOutboundResult carries the synchronous outcome of a single-node delay test.
// The error model is Variant B (SPEC §3.2): the application-level result lives in this
// struct, never in the returned Go error (which is reserved for transport failures).
// Source of truth is Error: Delay is valid iff Error == "". Delay == 0 with Error == ""
// is a successful 0 ms test, NOT a failure — a fast local node must not read as a fail.
type URLTestOutboundResult struct {
	Delay int32
	Error string
}

// URLTestOutbound measures the latency of a single node — an outbound OR an endpoint
// (WG/AWG/Tailscale) — over a caller-supplied link with a timeout in milliseconds (0 =
// core default, no explicit deadline). It mirrors the Clash /proxies/{name}/delay
// semantics that the trimmed Clash API used to provide. Mass-pinging N nodes is the
// caller's job (a worker pool in LxBox); the core measures one node synchronously and
// stateless. Cancellation (SPEC 015 §3.6): the core handler parents the test to the gRPC
// call ctx, so dropping the call aborts that in-flight test at its dial. The gomobile
// binding exposes no per-call cancel and this CommandClient shares one c.ctx across all
// calls, so mass-cancel is done by running the ping pool on a SEPARATE CommandClient
// instance and calling Disconnect() on it — its c.cancel()+conn.Close() reach the test
// ctx without touching other streams. No server-side batch RPC exists yet (deferred, see
// SPEC 015 §5). Detail: CLIENT_FEEDBACK_urltest_cancel_binding.md.
func (c *CommandClient) URLTestOutbound(outboundTag string, link string, timeout int32) (*URLTestOutboundResult, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*URLTestOutboundResult, error) {
		response, err := client.URLTestOutbound(ctx, &daemon.URLTestOutboundRequest{
			OutboundTag: outboundTag,
			Link:        link,
			Timeout:     uint32(timeout),
		})
		if err != nil {
			return nil, E.Cause(err, "url test outbound")
		}
		return &URLTestOutboundResult{
			Delay: int32(response.Delay),
			Error: response.Error,
		}, nil
	})
}

// GetRules returns a snapshot of the routing rule table — route rules and DNS rules,
// split by IsDNS. Route fields match the Clash /rules shape; DNS rules go beyond Clash,
// which never exposed them. The result is an iterator for gomobile binding parity with
// the other libbox list accessors.
func (c *CommandClient) GetRules() (RuleIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (RuleIterator, error) {
		ruleList, err := client.GetRules(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get rules")
		}
		rules := make([]*Rule, 0, len(ruleList.Rules))
		for _, rule := range ruleList.Rules {
			rules = append(rules, &Rule{
				Type:    rule.Type,
				Payload: rule.Payload,
				Action:  rule.Action,
				IsDNS:   rule.IsDNS,
			})
		}
		return newIterator(rules), nil
	})
}

// GetGroups returns a pull snapshot of the outbound groups — the same data the
// CommandGroup subscription pushes, but fetched synchronously on demand. SPEC 015 §3.3:
// the subscription only delivers an initial snapshot if its stream opened; a lost or
// never-opened stream left the client unable to re-read group state (empty main screen).
// This getter closes that gap without recreating the client. Reuses the same gRPC→libbox
// conversion as the subscription, so the returned iterator is identical in shape.
func (c *CommandClient) GetGroups() (OutboundGroupIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (OutboundGroupIterator, error) {
		groups, err := client.GetGroups(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get groups")
		}
		return outboundGroupIteratorFromGRPC(groups), nil
	})
}

// GetOutbounds returns a pull snapshot of the flat outbound/endpoint list — the same
// data SubscribeOutbounds pushes. Needed alongside GetGroups because standalone outbounds
// and endpoints (WG/AWG/Tailscale) appear only in this flat list, not in CommandGroup.
func (c *CommandClient) GetOutbounds() (OutboundGroupItemIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (OutboundGroupItemIterator, error) {
		list, err := client.GetOutbounds(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get outbounds")
		}
		return outboundGroupItemListFromGRPC(list), nil
	})
}

// Rule is the libbox view of one routing rule. Type/Payload/Action mirror the Clash
// /rules JSON; IsDNS distinguishes route rules (false) from DNS rules (true).
type Rule struct {
	Type    string
	Payload string
	Action  string
	IsDNS   bool
}

// RuleIterator binds the rule snapshot for gomobile (no slice support across the bridge).
type RuleIterator interface {
	Next() *Rule
	HasNext() bool
}

// PoolSlot is the libbox view of one round_robin rotation-pool slot (SPEC 019 v2). Slot is
// the fixed slot index; Tag is the node currently in it; Delay is its last test result in
// ms (0 = dead/not-measured — a live node is clamped to >= 1 server-side).
type PoolSlot struct {
	Slot  int32
	Tag   string
	Delay int32
}

// PoolSlotIterator binds the pool snapshot for gomobile.
type PoolSlotIterator interface {
	Next() *PoolSlot
	HasNext() bool
}

// GetPool returns the current round_robin rotation pool of a urltest group (SPEC 019 v2).
// A non-round_robin group (selector/least_test) or unknown tag yields an empty iterator —
// "no pool", not an error. Lets the UI show which N nodes are actually in rotation, with
// their delays, instead of the full 1000-node config list.
func (c *CommandClient) GetPool(groupTag string) (PoolSlotIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (PoolSlotIterator, error) {
		list, err := client.GetPool(ctx, &daemon.GetPoolRequest{GroupTag: groupTag})
		if err != nil {
			return nil, E.Cause(err, "get pool")
		}
		slots := make([]*PoolSlot, 0, len(list.Slots))
		for _, slot := range list.Slots {
			slots = append(slots, &PoolSlot{
				Slot:  int32(slot.Slot),
				Tag:   slot.Tag,
				Delay: int32(slot.Delay),
			})
		}
		return newIterator(slots), nil
	})
}

// DnsGroupMember is the libbox view of one DNS-group member's records
// (SPEC 035 v3). Clean = zero live errors; LastErrorAgeMs is the age of the
// newest live error (-1 = none); LiveWins are live win records (fastest);
// Current marks the group's sticky target; LastRTTMs is the last successful
// probe (0 = never measured).
type DnsGroupMember struct {
	Tag            string
	ServerType     string
	Clean          bool
	LiveErrors     int32
	LastErrorAgeMs int64
	LiveWins       int32
	Current        bool
	LastRTTMs      int32
}

type DnsGroupMemberIterator interface {
	Next() *DnsGroupMember
	HasNext() bool
}

// DnsGroup is the libbox view of one DNS group's state (SPEC 035 v3).
// Current is the sticky target ("" = not chosen yet / parallel — a valid
// state, not an error).
type DnsGroup struct {
	Tag     string
	Mode    string // "stable" | "fastest" | "parallel"
	Current string
	members []*DnsGroupMember
}

// Members returns the per-member record snapshot in config order.
func (g *DnsGroup) Members() DnsGroupMemberIterator {
	return newIterator(g.members)
}

type DnsGroupIterator interface {
	Next() *DnsGroup
	HasNext() bool
}

// GetDNSGroups returns a point-in-time health snapshot of every DNS group in the running
// config (SPEC 035). No groups in the config yields an empty iterator, not an error.
func (c *CommandClient) GetDNSGroups() (DnsGroupIterator, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (DnsGroupIterator, error) {
		list, err := client.GetDNSGroups(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, E.Cause(err, "get dns groups")
		}
		groups := make([]*DnsGroup, 0, len(list.Groups))
		for _, groupState := range list.Groups {
			groupView := &DnsGroup{
				Tag:     groupState.Tag,
				Mode:    groupState.Mode,
				Current: groupState.Current,
			}
			for _, memberState := range groupState.Members {
				groupView.members = append(groupView.members, &DnsGroupMember{
					Tag:            memberState.Tag,
					ServerType:     memberState.ServerType,
					Clean:          memberState.Clean,
					LiveErrors:     int32(memberState.LiveErrors),
					LastErrorAgeMs: memberState.LastErrorAgeMs,
					LiveWins:       int32(memberState.LiveWins),
					Current:        memberState.Current,
					LastRTTMs:      int32(memberState.LastRttMs),
				})
			}
			groups = append(groups, groupView)
		}
		return newIterator(groups), nil
	})
}
