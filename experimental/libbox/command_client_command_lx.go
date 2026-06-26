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
	DNSServer     string // which DNS server (transport) resolved this (SPEC 018)
	DNSServerType string // udp / tls / https / quic
	ProcessInfo   *ProcessInfo
	answers       []*DnsAnswer
	outbound      []string
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

// DnsQueryHandler receives DNS-query events. OnQuery fires per resolution; OnError on a
// stream failure (the subscription is then done).
type DnsQueryHandler interface {
	OnQuery(query *DnsQuery)
	OnError(message string)
}

// DnsQuerySubscription is the live handle; Close() stops the stream (mirrors the other
// libbox stream sessions).
type DnsQuerySubscription struct {
	streamSession
}

// SubscribeDNSQueries opens the structured DNS-query stream (SPEC 018). includeAnswers asks
// the core to attach the full answer set per event (heavier; off for plain attribution).
// On a core built without with_lx_command the call returns codes.Unimplemented, surfaced
// here as an error — the caller keeps whatever fallback it had.
func (c *CommandClient) SubscribeDNSQueries(includeAnswers bool, handler DnsQueryHandler) (*DnsQuerySubscription, error) {
	client, parentCtx, err := c.getClientForCall()
	if err != nil {
		return nil, E.Cause(err, "subscribe dns queries")
	}

	streamCtx, cancel := context.WithCancel(parentCtx)
	subscription := &DnsQuerySubscription{
		streamSession: streamSession{
			ctx:       streamCtx,
			cancel:    cancel,
			closeDone: make(chan struct{}),
		},
	}

	stream, err := client.SubscribeDNSQueries(streamCtx, &daemon.SubscribeDNSQueriesRequest{
		IncludeAnswers: includeAnswers,
	})
	if err != nil {
		cancel()
		if c.standalone {
			c.closeConnection()
		}
		return nil, E.Cause(err, "subscribe dns queries")
	}

	standalone := c.standalone
	go func() {
		defer func() {
			close(subscription.closeDone)
			if standalone {
				c.closeConnection()
			}
		}()
		for {
			event, recvErr := stream.Recv()
			if recvErr != nil {
				if subscription.ctx.Err() != nil {
					return
				}
				handler.OnError(E.Cause(recvErr, "dns query stream recv").Error())
				return
			}
			handler.OnQuery(dnsQueryFromGRPC(event))
		}
	}()
	return subscription, nil
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
		outbound:      event.Outbound,
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
