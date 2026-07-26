//go:build with_lx_command

package daemon

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing-box/common/urltest"
	dnsgroup "github.com/sagernet/sing-box/dns/transport/group"
	"github.com/sagernet/sing-box/protocol/group"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc"
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

	// Cancellation (SPEC 015 §3.6): the test is parented to the gRPC per-call ctx, NOT
	// boxService.ctx. gRPC cancels this ctx automatically when the client cancels the call
	// (or the conn drops), so a single in-flight test aborts at its dial/handshake without
	// touching other streams — the granular per-node cancel Clash had via r.Context().
	// Parenting to boxService.ctx (the old code) made the test outlive the call: client
	// cancel could not reach the dial, and the only lever was tearing down the whole conn.
	// timeout == 0 → bounded only by the call ctx (core default, no extra deadline);
	// > 0 → a bounded child context in milliseconds layered on top of the call ctx.
	testCtx := ctx
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		testCtx, cancel = context.WithTimeout(ctx, time.Duration(request.Timeout)*time.Millisecond)
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
// the same RLock the stream uses. Errors via status.Error(FailedPrecondition) when not
// started (as GetOutbounds/GetPool do) — not the Variant-B payload model of URLTestOutbound.
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

// poolProvider is any outbound exposing a rotation pool (SPEC 019 v2). Every urltest
// group implements it — Pool() is an unconditional method on *group.URLTest — but a
// least_test group returns nil (no balancer), so a non-round_robin urltest and any
// non-urltest outbound both fall through to an empty PoolList, not an error.
type poolProvider interface {
	Pool() []group.PoolSlot
}

// GetPool returns the current round_robin rotation pool of a urltest group (SPEC 019 v2).
// delay is clamped 0->1 for live nodes inside Pool(), so 0 in the response unambiguously
// means dead/not-measured. A non-round_robin group (or unknown tag) returns empty slots.
func (s *StartedService) GetPool(ctx context.Context, request *GetPoolRequest) (*PoolList, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	var list PoolList
	detour, loaded := boxService.outboundManager.Outbound(request.GroupTag)
	if !loaded {
		return &list, nil // unknown tag → empty pool (not an error)
	}
	provider, ok := detour.(poolProvider)
	if !ok {
		return &list, nil // not a urltest group → empty pool
	}
	for _, slot := range provider.Pool() {
		list.Slots = append(list.Slots, &PoolSlot{
			Slot:  uint32(slot.Slot),
			Tag:   slot.Tag,
			Delay: uint32(slot.Delay),
		})
	}
	return &list, nil
}

// dnsGroupStateProvider is the DNS-group transport's health snapshot hook (SPEC 035).
// Discovered by type-assertion over the transport manager's list — mirrors poolProvider:
// any transport not implementing it (every non-group type) is silently skipped.
type dnsGroupStateProvider interface {
	GroupState() dnsgroup.State
}

// GetDNSGroups returns a point-in-time record snapshot of every DNS group in the
// running config (SPEC 035 v3): mode, sticky target, and per-member records
// (clean, live errors with the age of the newest, live wins, last rtt). Groups
// are few and the UI draws them all, so there is no per-tag request. No groups
// (or no DNS transport manager) yields an empty list, not an error.
func (s *StartedService) GetDNSGroups(ctx context.Context, empty *emptypb.Empty) (*DnsGroupList, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	boxService := s.instance
	s.serviceAccess.RUnlock()

	var list DnsGroupList
	transportManager := service.FromContext[adapter.DNSTransportManager](boxService.ctx)
	if transportManager == nil {
		return &list, nil
	}
	for _, transport := range transportManager.Transports() {
		provider, isGroup := transport.(dnsGroupStateProvider)
		if !isGroup {
			continue
		}
		snapshot := provider.GroupState()
		groupProto := &DnsGroupState{
			Tag:     snapshot.Tag,
			Mode:    snapshot.Mode,
			Current: snapshot.Current,
		}
		for _, memberSnapshot := range snapshot.Members {
			memberProto := &DnsGroupMember{
				Tag:            memberSnapshot.Tag,
				ServerType:     memberSnapshot.ServerType,
				Clean:          memberSnapshot.Clean,
				LiveErrors:     uint32(memberSnapshot.LiveErrors),
				LastErrorAgeMs: -1,
				LiveWins:       uint32(memberSnapshot.LiveWins),
				Current:        memberSnapshot.Current,
				LastRttMs:      clampRttMs(memberSnapshot.LastRTT),
			}
			if memberSnapshot.HasError {
				memberProto.LastErrorAgeMs = memberSnapshot.LastErrorAge.Milliseconds()
			}
			groupProto.Members = append(groupProto.Members, memberProto)
		}
		list.Groups = append(list.Groups, groupProto)
	}
	return &list, nil
}

// GetRunningConfig returns the canonical serialization of the options the running box
// was actually built from (SPEC 037) — the post-override struct captured once in
// newInstance, NOT the profile text the client sent: tun AutoRedirect/packages and the
// injected OOM-killer service are included, field order / omitempty / [] -> null follow
// the re-marshal. This is the single source of truth for "what is running" — the client
// derives per-node JSON ("View details" / "Copy JSON") by extracting the tag from this
// document instead of a per-tag RPC. Unavailable (not FailedPrecondition) distinguishes
// "started but no snapshot" — the attached-service path (service/api) never captures one.
func (s *StartedService) GetRunningConfig(ctx context.Context, empty *emptypb.Empty) (*RunningConfig, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	content := s.instance.runningConfig
	s.serviceAccess.RUnlock()
	if content == "" {
		return nil, status.Error(codes.Unavailable, "running config was not captured for this service")
	}
	return &RunningConfig{Content: content}, nil
}

// clampRttMs converts a measured rtt to ms, clamping sub-millisecond
// measurements to 1 so that 0 unambiguously means "never measured"
// (precedent: GetPool clamps live-node delay 0->1).
func clampRttMs(rtt time.Duration) uint32 {
	if rtt <= 0 {
		return 0
	}
	if ms := rtt.Milliseconds(); ms > 0 {
		return uint32(ms)
	}
	return 1
}

// SubscribeDNSQueries streams structured, process-attributed DNS resolutions (SPEC 018).
// Unlike SubscribeConnections it is event-driven, not ticked: each resolution in the core
// emits one QueryEvent (dns/client_log.go), forwarded here as it arrives. The dnstrack
// manager lives in the box service context, registered only when observability is on; a
// missing manager means DNS tracking is unavailable (mirrors the connections guard).
func (s *StartedService) SubscribeDNSQueries(request *SubscribeDNSQueriesRequest, server grpc.ServerStreamingServer[DnsQueryEvent]) error {
	if err := s.waitForStarted(server.Context()); err != nil {
		return err
	}
	s.serviceAccess.RLock()
	boxService := s.instance
	s.serviceAccess.RUnlock()

	// PtrFromContext (not FromContext[*T]): the manager is registered via MustRegisterPtr
	// in box.go, which keys on *dnstrack.Manager. FromContext[*dnstrack.Manager] would key
	// on **dnstrack.Manager and never find it — that was the §180 Unimplemented bug. This
	// mirrors how trafficManager is resolved (daemon/instance.go PtrFromContext).
	manager := service.PtrFromContext[dnstrack.Manager](boxService.ctx)
	if manager == nil {
		return status.Error(codes.Unimplemented, "DNS query tracking not available")
	}

	subscription, done, err := manager.SubscribeEvents()
	if err != nil {
		return err
	}
	defer manager.UnSubscribeEvents(subscription)

	includeAnswers := request.GetIncludeAnswers()
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-server.Context().Done():
			return server.Context().Err()
		case <-done:
			return nil
		case event := <-subscription:
			if err := server.Send(dnsQueryEventToProto(event, includeAnswers, boxService.outboundManager)); err != nil {
				return err
			}
		}
	}
}

// dnsQueryEventToProto maps a core dnstrack event to the wire message, reusing the shared
// ProcessInfo shape. answers are dropped unless the subscriber asked for them — the core
// always captures them (cheap, the message is already parsed), the per-subscriber filter
// lives here so one emit serves every subscriber. SPEC 018.
func dnsQueryEventToProto(event dnstrack.QueryEvent, includeAnswers bool, outboundManager adapter.OutboundManager) *DnsQueryEvent {
	proto := &DnsQueryEvent{
		Domain:        event.Domain,
		QueryType:     uint32(event.QueryType),
		Rcode:         event.Rcode,
		Ttl:           event.TTL,
		Source:        string(event.Source),
		Failed:        event.Failed,
		Error:         event.Error,
		DnsServer:     event.DNSServer,
		DnsServerType: event.DNSServerType,
		Outbound:      resolveOutboundChain(event.Outbound, outboundManager),
		DnsGroupPath:  event.GroupPath,
		Fanned:        event.Fanned,
		Survival:      event.Survival,
	}
	for _, attempt := range event.Attempts { // SPEC 035 — probe trace, vocabulary passed through verbatim
		proto.Attempts = append(proto.Attempts, &DnsGroupAttempt{
			Server:     attempt.Server,
			ServerType: attempt.ServerType,
			Outcome:    attempt.Outcome,
			RttMs:      attempt.RTTMs,
		})
	}
	if event.ProcessInfo != nil {
		proto.ProcessInfo = &ProcessInfo{
			ProcessId:    event.ProcessInfo.ProcessID,
			UserId:       event.ProcessInfo.UserId,
			UserName:     event.ProcessInfo.UserName,
			ProcessPath:  event.ProcessInfo.ProcessPath,
			PackageNames: event.ProcessInfo.AndroidPackageNames,
		}
	}
	if includeAnswers && len(event.Answers) > 0 {
		proto.Answers = make([]*DnsAnswer, 0, len(event.Answers))
		for _, answer := range event.Answers {
			proto.Answers = append(proto.Answers, &DnsAnswer{
				Name:  answer.Name,
				Type:  uint32(answer.Type),
				Rdata: answer.RData,
				Ttl:   answer.TTL,
			})
		}
	}
	return proto
}

// resolveOutboundChain expands the DNS server's static detour tag to the live channel:
// if the tag is a selector/urltest group, append its active Now() node so the client sees
// the real outbound, not just the group name (consistent with Connection.Detour, SPEC 017).
// Done here, server-side, only for subscribers — never on the DNS hot path. Empty in →
// empty out (cached/optimistic, where the query never left).
func resolveOutboundChain(tags []string, outboundManager adapter.OutboundManager) []string {
	if len(tags) == 0 {
		return nil
	}
	chain := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		chain = append(chain, tag)
		detour, loaded := outboundManager.Outbound(tag)
		if !loaded {
			continue
		}
		if group, isGroup := detour.(adapter.OutboundGroup); isGroup {
			if now := group.Now(); now != "" {
				chain = append(chain, now)
			}
		}
	}
	return chain
}
