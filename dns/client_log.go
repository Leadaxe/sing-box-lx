package dns

import (
	"context"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"

	"github.com/miekg/dns"
)

// emitQueryEvent publishes a structured DNS-query event to the lx dnstrack stream
// (SPEC 018). It runs alongside the text log at every successful resolution outcome;
// ProcessInfo is pulled from the request context (already populated by the router's
// process search), so the event carries app attribution the text log lacks. A missing
// manager (observability off / core built without it) is a no-op via the nil receiver.
//
// Answers carries the entire response.Answer in wire order (CNAME hops + final A/AAAA), so
// a client can rebuild the CNAME chain from one event. The per-subscriber includeAnswers
// flag is applied by the command server, not here — the emit is shared across subscribers.
func emitQueryEvent(ctx context.Context, transport adapter.DNSTransport, response *dns.Msg, source dnstrack.Source, ttl uint32) {
	manager := service.PtrFromContext[dnstrack.Manager](ctx)
	// HasSubscribers gate: with no profiler attached, build nothing — not the event, not the
	// answers slice, not the outbound tag. The query never left for cached/optimistic, so its
	// channel (outbound) is left empty there; dnsServer/type still identify the resolver.
	if manager == nil || !manager.HasSubscribers() || response == nil || len(response.Question) == 0 {
		return
	}
	question := response.Question[0]
	event := dnstrack.QueryEvent{
		Domain:      FqdnToDomain(question.Name),
		QueryType:   question.Qtype,
		Rcode:       int32(response.Rcode),
		TTL:         ttl,
		Source:      source,
		ProcessInfo: processInfoFromContext(ctx),
		Answers:     answersFromMessage(response),
	}
	if transport != nil {
		event.DNSServer = transport.Tag()
		event.DNSServerType = transport.Type()
		// Outbound only where the query actually went out — not cached/optimistic.
		if source == dnstrack.SourceExchanged || source == dnstrack.SourceRefreshed {
			if tag := transport.OutboundTag(); tag != "" {
				event.Outbound = []string{tag}
			}
		}
		// SPEC 035: a composite transport (group) records the member that
		// actually answered plus the probe trace; prefer the member — the group
		// tag alone makes per-member diagnostics impossible. Cache hits never
		// touch the trace, so they keep the group tag (the answer came from the
		// group's cache, not from a member) and an empty trace.
		if trace, hasTrace := dnstrack.SnapshotTrace(ctx); hasTrace {
			if trace.EffectiveTag != "" {
				event.DNSServer = trace.EffectiveTag
				event.DNSServerType = trace.EffectiveType
				if (source == dnstrack.SourceExchanged || source == dnstrack.SourceRefreshed) && trace.EffectiveOutbound != "" {
					event.Outbound = []string{trace.EffectiveOutbound}
				}
			}
			event.GroupPath = trace.GroupPath
			event.Attempts = trace.Attempts
			event.Fanned = trace.Fanned
			event.Survival = trace.Survival
		}
	}
	manager.Emit(event)
}

// emitFailedQuery publishes a failure event from the resolver path where response is nil
// or rejected (SPEC 018 пункт 1) — timeout, loopback, rejected-cached, SERVFAIL-reject.
// Without this the stream would silently drop every DNS failure, the primary diagnostic
// signal. domain/qtype come from the request question (a failed exchange has no response).
// rcode is the real code when a response exists, RcodeNoAnswer (-1) when it does not.
func emitFailedQuery(ctx context.Context, transport adapter.DNSTransport, question dns.Question, rcode int32, cause string) {
	manager := service.PtrFromContext[dnstrack.Manager](ctx)
	if manager == nil || !manager.HasSubscribers() {
		return
	}
	event := dnstrack.QueryEvent{
		Domain:      FqdnToDomain(question.Name),
		QueryType:   question.Qtype,
		Rcode:       rcode,
		Source:      dnstrack.SourceFailed,
		Failed:      true,
		Error:       cause,
		ProcessInfo: processInfoFromContext(ctx),
	}
	if transport != nil {
		event.DNSServer = transport.Tag()
		event.DNSServerType = transport.Type()
		// A failed exchange still went out (timeout/SERVFAIL), so the outbound applies;
		// loopback/rejected-cached never left, but transport is the intended server either way.
		if tag := transport.OutboundTag(); tag != "" {
			event.Outbound = []string{tag}
		}
		// SPEC 035: on a group SERVFAIL-reject the trace names the member that
		// produced the rejected answer; a total group failure leaves effective
		// unset and the group tag stands (no member answered — that IS the
		// state), while the trace still shows the probes that were made.
		if trace, hasTrace := dnstrack.SnapshotTrace(ctx); hasTrace {
			if trace.EffectiveTag != "" {
				event.DNSServer = trace.EffectiveTag
				event.DNSServerType = trace.EffectiveType
				if trace.EffectiveOutbound != "" {
					event.Outbound = []string{trace.EffectiveOutbound}
				}
			}
			event.GroupPath = trace.GroupPath
			event.Attempts = trace.Attempts
			event.Fanned = trace.Fanned
			event.Survival = trace.Survival
		}
	}
	manager.Emit(event)
}

func processInfoFromContext(ctx context.Context) *adapter.ConnectionOwner {
	if metadata := adapter.ContextFrom(ctx); metadata != nil {
		return metadata.ProcessInfo
	}
	return nil
}

// answersFromMessage flattens response.Answer to the wire-order RR list, preserving CNAME
// hops (NOT filtered down to A/AAAA — the chain would be lost). SPEC 018 Q2.
//
// RData is the BARE record value (the CNAME target / IP), not the full RR string. miekg's
// RR.String() returns "name TTL IN TYPE rdata"; we strip the header prefix (its String()
// is exactly that leading part) so the client gets "64.233.165.139", not
// "google.com. 29 IN A 64.233.165.139" (§180-2 had the client parsing the last field).
func answersFromMessage(response *dns.Msg) []dnstrack.Answer {
	if len(response.Answer) == 0 {
		return nil
	}
	answers := make([]dnstrack.Answer, 0, len(response.Answer))
	for _, record := range response.Answer {
		header := record.Header()
		rdata := strings.TrimPrefix(record.String(), header.String())
		answers = append(answers, dnstrack.Answer{
			Name:  FqdnToDomain(header.Name),
			Type:  header.Rrtype,
			RData: rdata,
			TTL:   header.Ttl,
		})
	}
	return answers
}

func logCachedResponse(logger logger.ContextLogger, ctx context.Context, transport adapter.DNSTransport, response *dns.Msg, ttl int) {
	if logger == nil || len(response.Question) == 0 {
		return
	}
	domain := FqdnToDomain(response.Question[0].Name)
	logger.DebugContext(ctx, "cached ", domain, " ", dns.RcodeToString[response.Rcode], " ", ttl)
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			logger.InfoContext(ctx, "cached ", dns.Type(record.Header().Rrtype).String(), " ", FormatQuestion(record.String()))
		}
	}
	emitQueryEvent(ctx, transport, response, dnstrack.SourceCached, uint32(ttl)) // lx: SPEC 018
}

func logOptimisticResponse(logger logger.ContextLogger, ctx context.Context, transport adapter.DNSTransport, response *dns.Msg) {
	if logger == nil || len(response.Question) == 0 {
		return
	}
	domain := FqdnToDomain(response.Question[0].Name)
	logger.DebugContext(ctx, "optimistic ", domain, " ", dns.RcodeToString[response.Rcode])
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			logger.InfoContext(ctx, "optimistic ", dns.Type(record.Header().Rrtype).String(), " ", FormatQuestion(record.String()))
		}
	}
	emitQueryEvent(ctx, transport, response, dnstrack.SourceOptimistic, 0) // lx: SPEC 018 (optimistic carries no fresh TTL)
}

func logExchangedResponse(logger logger.ContextLogger, ctx context.Context, transport adapter.DNSTransport, response *dns.Msg, ttl uint32) {
	if logger == nil || len(response.Question) == 0 {
		return
	}
	domain := FqdnToDomain(response.Question[0].Name)
	logger.DebugContext(ctx, "exchanged ", domain, " ", dns.RcodeToString[response.Rcode], " ", ttl)
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			logger.InfoContext(ctx, "exchanged ", dns.Type(record.Header().Rrtype).String(), " ", FormatQuestion(record.String()))
		}
	}
	emitQueryEvent(ctx, transport, response, dnstrack.SourceExchanged, ttl) // lx: SPEC 018
}

func logRefreshedResponse(logger logger.ContextLogger, ctx context.Context, transport adapter.DNSTransport, response *dns.Msg, ttl uint32) {
	if logger == nil || len(response.Question) == 0 {
		return
	}
	domain := FqdnToDomain(response.Question[0].Name)
	logger.DebugContext(ctx, "refreshed ", domain, " ", dns.RcodeToString[response.Rcode], " ", ttl)
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			logger.InfoContext(ctx, "refreshed ", dns.Type(record.Header().Rrtype).String(), " ", FormatQuestion(record.String()))
		}
	}
	emitQueryEvent(ctx, transport, response, dnstrack.SourceRefreshed, ttl) // lx: SPEC 018
}


func logRejectedResponse(logger logger.ContextLogger, ctx context.Context, response *dns.Msg) {
	if logger == nil || len(response.Question) == 0 {
		return
	}
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			logger.InfoContext(ctx, "rejected ", dns.Type(record.Header().Rrtype).String(), " ", FormatQuestion(record.String()))
		}
	}
	// lx: SPEC 018 — the rejected case is emitted as a failure (SourceFailed) from
	// client.go Exchange via emitFailedQuery, not here, to avoid a double emit.
}

func FqdnToDomain(fqdn string) string {
	if dns.IsFqdn(fqdn) {
		return fqdn[:len(fqdn)-1]
	}
	return fqdn
}

func FormatQuestion(string string) string {
	for strings.HasPrefix(string, ";") {
		string = string[1:]
	}
	string = strings.ReplaceAll(string, "\t", " ")
	string = strings.ReplaceAll(string, "\n", " ")
	string = strings.ReplaceAll(string, ";; ", " ")
	string = strings.ReplaceAll(string, "; ", " ")

	for strings.Contains(string, "  ") {
		string = strings.ReplaceAll(string, "  ", " ")
	}
	return strings.TrimSpace(string)
}
