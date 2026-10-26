package dns

// lx: SPEC 139 — the `dns` stage of a connection test: one stage per query to
// a DNS server. beginExchange opens it right before the transport is asked
// (traceExchangeBegin); the emit sites of SPEC 018 close it (traceExchanged,
// traceFailed), and a cache hit records its own REUSED stage (traceCached).
// The rule match of the DNS router lands on the enclosing stage
// (traceDNSRule). Without a trace in the context every helper returns after
// one ctx.Value lookup and builds nothing (SPEC 139 §5.12).

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"

	"github.com/miekg/dns"
)

type dnsStageKey struct{}

// dnsStageState ties the open stage to its exchange. A nested exchange (the
// DNS server's own domain resolved under its detour) runs on a descendant
// context and shadows the key with its own state; the transport/question pair
// keeps an emit made on an outer context (loopback, rejected-cached) from
// closing the stage of an exchange it does not belong to.
type dnsStageState struct {
	stage     *lxtrace.Stage
	transport string
	question  dns.Question
}

// traceExchangeBegin opens the `dns` stage of one exchange and returns the
// context the transport is asked on, so the stages of the server's own dial
// (detour, lookup, TLS) become its descendants.
func traceExchangeBegin(ctx context.Context, transport adapter.DNSTransport, question dns.Question) context.Context {
	if lxtrace.From(ctx) == nil {
		return ctx
	}
	ctx, stage := lxtrace.Begin(ctx, "dns", transport.Tag())
	if stage == nil {
		return ctx
	}
	stage.Attr("type", transport.Type())
	stage.Attr("question", questionText(question))
	if outbound := transport.OutboundTag(); outbound != "" {
		stage.Attr("outbound", outbound)
	}
	if transport.Type() == C.DNSTypeGroup {
		// SPEC 035: the group writes the answering member and its probes into
		// the holder; attach one when no SubscribeDNSQueries stream did.
		if _, hasTrace := dnstrack.SnapshotTrace(ctx); !hasTrace {
			ctx = dnstrack.WithQueryTrace(ctx)
		}
	}
	return context.WithValue(ctx, dnsStageKey{}, &dnsStageState{
		stage:     stage,
		transport: transport.Tag(),
		question:  question,
	})
}

// ownStage returns the stage opened for this transport/question in ctx.
func ownStage(ctx context.Context, transport adapter.DNSTransport, question dns.Question) *lxtrace.Stage {
	state, _ := ctx.Value(dnsStageKey{}).(*dnsStageState)
	if state == nil || transport == nil || state.transport != transport.Tag() || state.question != question {
		return nil
	}
	return state.stage
}

// traceResponse closes the stage on an answer from the network, or records a
// REUSED stage for an answer served from the cache.
func traceResponse(ctx context.Context, transport adapter.DNSTransport, response *dns.Msg, source dnstrack.Source, ttl uint32) {
	if lxtrace.From(ctx) == nil || transport == nil || len(response.Question) == 0 {
		return
	}
	question := response.Question[0]
	switch source {
	case dnstrack.SourceExchanged:
		stage := ownStage(ctx, transport, question)
		if stage == nil {
			return
		}
		attrResponse(stage, response)
		stage.Attr("ttl", strconv.FormatUint(uint64(ttl), 10))
		attrGroup(ctx, stage)
		stage.End(nil)
	case dnstrack.SourceCached, dnstrack.SourceOptimistic:
		_, stage := lxtrace.Begin(ctx, "dns", transport.Tag())
		if stage == nil {
			return
		}
		stage.Attr("type", transport.Type())
		stage.Attr("question", questionText(question))
		stage.Attr("source", "cache")
		if source == dnstrack.SourceOptimistic {
			// a stale answer served while a background refresh runs
			stage.Attr("stale", "true")
		} else {
			stage.Attr("ttl", strconv.FormatUint(uint64(ttl), 10))
		}
		attrResponse(stage, response)
		stage.Mark(lxtrace.Reused)
	}
}

// traceFailed closes the stage of a failed exchange. A failure reported before
// the exchange opened its stage (loopback, rejected-cached) gets a stage of
// its own, so the failure is never lost from the trace.
func traceFailed(ctx context.Context, transport adapter.DNSTransport, question dns.Question, rcode int32, cause string) {
	if lxtrace.From(ctx) == nil || transport == nil {
		return
	}
	stage := ownStage(ctx, transport, question)
	if stage == nil {
		_, stage = lxtrace.Begin(ctx, "dns", transport.Tag())
		if stage == nil {
			return
		}
		stage.Attr("type", transport.Type())
		stage.Attr("question", questionText(question))
	}
	if rcode != dnstrack.RcodeNoAnswer {
		stage.Attr("rcode", rcodeText(int(rcode)))
	}
	attrGroup(ctx, stage)
	stage.End(errors.New(cause))
}

// traceDNSRule records the DNS rule the router matched on the enclosing stage
// (`app_dns` or `lookup`). A nil rule is the final (default) server.
func traceDNSRule(ctx context.Context, ruleIndex int, rule adapter.DNSRule) {
	stage := lxtrace.Current(ctx)
	if stage == nil {
		return
	}
	var value string
	if rule == nil {
		value = "final"
	} else {
		value = "dns[" + strconv.Itoa(ruleIndex) + "]"
		if description := rule.String(); description != "" {
			value += " " + description
		}
		if action := rule.Action(); action != nil {
			value += " => " + action.String()
		}
	}
	// Both families of a dual-stack lookup share the enclosing stage: name the
	// query type the match was made for.
	if metadata := adapter.ContextFrom(ctx); metadata != nil && metadata.QueryType != 0 {
		value += " (" + dns.Type(metadata.QueryType).String() + ")"
	}
	stage.Attr("dns_rule", value)
}

func attrResponse(stage *lxtrace.Stage, response *dns.Msg) {
	stage.Attr("rcode", rcodeText(response.Rcode))
	stage.Attr("answers", strconv.Itoa(len(response.Answer)))
}

// attrGroup copies the SPEC 035 attribution of a group transport: the member
// that answered, the group path and the member probes.
func attrGroup(ctx context.Context, stage *lxtrace.Stage) {
	trace, hasTrace := dnstrack.SnapshotTrace(ctx)
	if !hasTrace {
		return
	}
	if trace.EffectiveTag != "" {
		stage.Attr("member", trace.EffectiveTag)
	}
	if len(trace.GroupPath) > 0 {
		stage.Attr("group_path", strings.Join(trace.GroupPath, ","))
	}
	if len(trace.Attempts) > 0 {
		attempts := make([]string, 0, len(trace.Attempts))
		for _, attempt := range trace.Attempts {
			attempts = append(attempts, attempt.Server+":"+attempt.Outcome+":"+strconv.FormatUint(uint64(attempt.RTTMs), 10)+"ms")
		}
		stage.Attr("probes", strings.Join(attempts, ","))
	}
	if trace.Fanned {
		stage.Attr("fanned", "true")
	}
	if trace.Survival {
		stage.Attr("survival", "true")
	}
}

func questionText(question dns.Question) string {
	return dns.Type(question.Qtype).String() + " " + FqdnToDomain(question.Name)
}

func rcodeText(rcode int) string {
	if text, loaded := dns.RcodeToString[rcode]; loaded {
		return text
	}
	return strconv.Itoa(rcode)
}
