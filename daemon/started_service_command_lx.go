//go:build with_lx_command

package daemon

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/protocol/group"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SPEC 014 — libbox command-protocol extensions (CONSTITUTION §3.6). These handlers
// bridge two kernel capabilities that upstream only exposes through the (trimmed)
// Clash API: per-node delay testing and a route/DNS rule-table snapshot. They are a
// pure bridge — no new subsystem, no inbound/server logic. The build-tag twin
// started_service_command_lx_stub.go returns codes.Unimplemented when with_lx_command
// is absent, keeping tag-less builds behaviourally equivalent to upstream.

// URLTestOutbound measures the latency of a single node — an outbound OR an endpoint
// (WG/AWG/Tailscale) — with a caller-supplied URL and timeout, returning a synchronous
// {delay, error}. Unlike the group-level URLTest RPC it never type-asserts to
// adapter.OutboundGroup; an endpoint embeds adapter.Outbound (and thus N.Dialer), so it
// flows straight into urltest.URLTest without wrappers.
//
// Error model — Variant B (SPEC §3.2): every application-level outcome is reported in the
// response payload, never as a transport gRPC error, so the handler always returns
// (resp, nil). The source of truth for the client is the error field: delay is valid
// iff error == "". delay==0 && error=="" is success at 0 ms, NOT a failure (a fast
// local node must not read as a false fail).
func (s *StartedService) URLTestOutbound(ctx context.Context, request *URLTestOutboundRequest) (*URLTestOutboundResponse, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, os.ErrInvalid
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	tag := request.OutboundTag
	// Resolve in BOTH managers: outbound first, then endpoint. adapter.Endpoint embeds
	// adapter.Outbound, so either resolution yields an N.Dialer for urltest.URLTest and
	// an adapter.Outbound for group.RealTag (history keying).
	var detour N.Dialer
	var realTagSource adapter.Outbound
	if outbound, isLoaded := boxService.outboundManager.Outbound(tag); isLoaded {
		detour = outbound
		realTagSource = outbound
	} else if endpoint, isLoaded := boxService.endpointManager.Get(tag); isLoaded {
		detour = endpoint
		realTagSource = endpoint
	} else {
		return &URLTestOutboundResponse{Error: "outbound or endpoint not found: " + tag}, nil
	}

	// timeout == 0 → the long-lived service context (no explicit deadline, core default);
	// > 0 → a bounded child context in milliseconds. Cancellation of an in-flight mass
	// ping is the client closing the conn (boxService.ctx then drives the dial down).
	testCtx := boxService.ctx
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		testCtx, cancel = context.WithTimeout(boxService.ctx, time.Duration(request.Timeout)*time.Millisecond)
		defer cancel()
	}

	delay, err := urltest.URLTest(testCtx, request.Link, detour)

	realTag := group.RealTag(realTagSource)
	if err != nil {
		boxService.urlTestHistoryStorage.DeleteURLTestHistory(realTag)
		return &URLTestOutboundResponse{Error: err.Error()}, nil
	}
	boxService.urlTestHistoryStorage.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
		Time:  time.Now(),
		Delay: delay,
	})
	return &URLTestOutboundResponse{Delay: uint32(delay)}, nil
}

// GetRules returns a snapshot of the routing rule table — route rules and DNS rules,
// distinguished by isDNS. Rules are static for the lifetime of a config, so this is a
// unary snapshot (no stream). Route fields match the Clash /rules shape; DNS rules go
// further than Clash, which does not expose them at all.
func (s *StartedService) GetRules(ctx context.Context, empty *emptypb.Empty) (*RuleList, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, os.ErrInvalid
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	routeRules := boxService.instance.Router().Rules()
	rules := make([]*Rule, 0, len(routeRules))
	for _, rule := range routeRules {
		rules = append(rules, &Rule{
			Type:    rule.Type(),
			Payload: rule.String(),
			Action:  rule.Action().String(),
			IsDNS:   false,
		})
	}
	if dnsRouter := service.FromContext[adapter.DNSRouter](boxService.ctx); dnsRouter != nil {
		for _, rule := range dnsRouter.Rules() {
			rules = append(rules, &Rule{
				Type:    rule.Type(),
				Payload: rule.String(),
				Action:  rule.Action().String(),
				IsDNS:   true,
			})
		}
	}
	return &RuleList{Rules: rules}, nil
}

// GetGroups returns a pull snapshot of the outbound groups — the same payload
// SubscribeGroups emits as its first message. SPEC 015 §3.3: the CommandClient is
// push-only, so if the SubscribeGroups stream never opened (service not yet STARTED
// at subscribe time) or broke, the client had no cheap way to re-read group state and
// the main screen stayed empty (tunnel connected, groups=[]). This unary getter closes
// that gap without recreating the whole client / tearing down other streams. It reuses
// the existing s.readGroups() builder (single source, also fixes the len<2 drop) under
// the same RLock the stream uses. Errors via status.Error (unary read convention, like
// GetRules — not the Variant-B payload model of URLTestOutbound).
func (s *StartedService) GetGroups(ctx context.Context, empty *emptypb.Empty) (*Groups, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	groups := s.readGroups()
	s.serviceAccess.RUnlock()
	return groups, nil
}

// GetOutbounds returns a pull snapshot of the flat outbound/endpoint list — the same
// payload SubscribeOutbounds emits. Needed alongside GetGroups (SPEC 015 §3.4) because
// SubscribeGroups covers only in-group nodes, whereas standalone outbounds and any
// endpoints (WG/AWG/Tailscale) appear only here. No readOutbounds() helper exists —
// SubscribeOutbounds builds OutboundList inline — so the builder is duplicated here
// rather than refactoring the upstream started_service.go.
func (s *StartedService) GetOutbounds(ctx context.Context, empty *emptypb.Empty) (*OutboundList, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	historyStorage := boxService.urlTestHistoryStorage
	var list OutboundList
	appendItem := func(detour adapter.Outbound) {
		item := &GroupItem{Tag: detour.Tag(), Type: detour.Type()}
		if history := historyStorage.LoadURLTestHistory(adapter.OutboundTag(detour)); history != nil {
			item.UrlTestTime = history.Time.Unix()
			item.UrlTestDelay = int32(history.Delay)
		}
		list.Outbounds = append(list.Outbounds, item)
	}
	for _, ob := range boxService.outboundManager.Outbounds() {
		appendItem(ob)
	}
	for _, ep := range boxService.endpointManager.Endpoints() {
		appendItem(ep) // adapter.Endpoint embeds adapter.Outbound
	}
	return &list, nil
}
