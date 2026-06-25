package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/protobuf/types/known/emptypb"
)

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
// stateless. Cancelling an in-flight batch means closing the CommandClient connection.
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
