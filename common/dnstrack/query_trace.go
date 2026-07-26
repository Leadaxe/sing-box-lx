package dnstrack

import (
	"context"
	"sync"

	"github.com/sagernet/sing/service"
)

// QueryTrace is the per-request mutable trace of a query that travels through
// composite DNS transports (the lx `group` type, SPEC 035/036). The client
// inserts the holder into the operation context before calling the transport;
// groups fill it as they probe members; the emit sites read a snapshot under
// the mutex. The holder must live in a context ANCESTOR of the transport call:
// values written by children are invisible upstream, so a group cannot attach
// it itself.
//
// Writes arriving after the snapshot was taken (race stragglers finishing in
// the background) mutate only the holder and are harmlessly lost — by design:
// the event is "what the query saw when it was answered"; the full picture
// lives in the GetDNSGroups state RPC.
type QueryTrace struct {
	access sync.Mutex

	// effective* identify the member that ACTUALLY answered. Write-once: the
	// innermost group answers first and its leaf member must win — with
	// last-write-wins an outer group would overwrite the leaf with the inner
	// group's own tag (the SPEC 036 nested-attribution fix).
	effectiveTag      string
	effectiveType     string
	effectiveOutbound string

	groupPath []string  // inside-out; built by prepending on group entry
	attempts  []Attempt // chronology of member probes, appended as they resolve
	racer     bool      // this query triggered a race fan-out
}

// Attempt is one resolved member probe.
type Attempt struct {
	Server     string
	ServerType string
	Outcome    string // AttemptAnswered | AttemptTimeout | AttemptNetworkError | AttemptServfail
	RTTMs      uint32
}

// Attempt outcomes — the single wire vocabulary (daemon forwards these
// strings verbatim, no second mapping).
const (
	AttemptAnswered     = "answered"
	AttemptTimeout      = "timeout"
	AttemptNetworkError = "network_error"
	AttemptServfail     = "servfail"
)

// TraceSnapshot is an immutable copy of the trace at emit time.
type TraceSnapshot struct {
	EffectiveTag      string
	EffectiveType     string
	EffectiveOutbound string
	GroupPath         []string
	Attempts          []Attempt
	Racer             bool
}

type queryTraceKey struct{}

// WithQueryTraceIfTracking attaches a fresh trace holder only when somebody
// is listening: without a dnstrack manager in ctx or without an open
// SubscribeDNSQueries stream it returns ctx unchanged, so a query with no
// subscribers pays one lookup and nothing else.
func WithQueryTraceIfTracking(ctx context.Context) context.Context {
	manager := service.PtrFromContext[Manager](ctx)
	if manager == nil || !manager.HasSubscribers() {
		return ctx
	}
	return WithQueryTrace(ctx)
}

// WithQueryTrace attaches a holder unconditionally (tests and callers that
// already checked the subscription gate). One holder = one query operation.
func WithQueryTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryTraceKey{}, &QueryTrace{})
}

func traceFrom(ctx context.Context) *QueryTrace {
	trace, _ := ctx.Value(queryTraceKey{}).(*QueryTrace)
	return trace
}

// SetEffectiveServer records the answering member. Write-once: later calls
// (outer groups unwinding) are dropped. No-op without a holder.
func SetEffectiveServer(ctx context.Context, tag string, serverType string, outboundTag string) {
	trace := traceFrom(ctx)
	if trace == nil {
		return
	}
	trace.access.Lock()
	if trace.effectiveTag == "" {
		trace.effectiveTag = tag
		trace.effectiveType = serverType
		trace.effectiveOutbound = outboundTag
	}
	trace.access.Unlock()
}

// PushGroup records that the query entered a group. Groups are entered
// outermost-first, so PREPENDING yields the inside-out order the stream
// promises without a reversal at emit time.
func PushGroup(ctx context.Context, tag string) {
	trace := traceFrom(ctx)
	if trace == nil {
		return
	}
	trace.access.Lock()
	trace.groupPath = append([]string{tag}, trace.groupPath...)
	trace.access.Unlock()
}

// RecordAttempt appends one resolved probe. Safe from background goroutines
// (a race collector keeps recording after the racer already returned).
func RecordAttempt(ctx context.Context, attempt Attempt) {
	trace := traceFrom(ctx)
	if trace == nil {
		return
	}
	trace.access.Lock()
	trace.attempts = append(trace.attempts, attempt)
	trace.access.Unlock()
}

// MarkRacer flags the query that triggered a race fan-out.
func MarkRacer(ctx context.Context) {
	trace := traceFrom(ctx)
	if trace == nil {
		return
	}
	trace.access.Lock()
	trace.racer = true
	trace.access.Unlock()
}

// SnapshotTrace returns an immutable copy of the trace; ok is false when the
// context carries no holder (direct exchanges, no subscribers). Slices are
// copied — the background race collector keeps appending to the holder after
// the event left.
func SnapshotTrace(ctx context.Context) (TraceSnapshot, bool) {
	trace := traceFrom(ctx)
	if trace == nil {
		return TraceSnapshot{}, false
	}
	trace.access.Lock()
	defer trace.access.Unlock()
	return TraceSnapshot{
		EffectiveTag:      trace.effectiveTag,
		EffectiveType:     trace.effectiveType,
		EffectiveOutbound: trace.effectiveOutbound,
		GroupPath:         append([]string(nil), trace.groupPath...),
		Attempts:          append([]Attempt(nil), trace.attempts...),
		Racer:             trace.racer,
	}, true
}
