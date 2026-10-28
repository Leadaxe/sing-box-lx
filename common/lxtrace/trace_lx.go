//go:build with_lx_command

package lxtrace

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/service"
)

// Result is the outcome of a stage. Numeric values equal the proto enum
// ConnectionTestStageResult (SPEC 139 §3.1).
type Result int

const (
	Running Result = iota
	OK
	Failed
	Skipped      // not run: an earlier stage failed or the depth does not reach it
	Reused       // not run: something ready was used (cache, live connection, tunnel already up)
	NoAck        // run, but the protocol does not confirm its success
	NotEvaluated // not computed: data was missing
)

// Depth is how far the test goes. Numeric values equal the proto enum
// ConnectionTestDepth (SPEC 139 §3.1).
type Depth int

const (
	DepthHTTP    Depth = iota // the full path up to the HTTP response
	DepthDNS                  // only the app's DNS queries
	DepthRoute                // DNS + rule, group and node selection; nothing is dialed
	DepthConnect              // up to an established tunnel, no request to the target
)

// Attr is one key/value attribute of a stage.
type Attr struct {
	Key   string
	Value string
}

// StageSnapshot is an immutable copy of a stage, emitted when the stage
// begins (Running) and when it ends (same ID), and collected in Summary.
type StageSnapshot struct {
	ID         uint32
	Parent     uint32
	Kind       string
	Subject    string
	StartMs    uint32
	DurationMs uint32
	Result     Result
	Error      string
	Attrs      []Attr
}

// Summary is the final state of a trace after Finish.
type Summary struct {
	Stages    []StageSnapshot
	RootCause uint32 // id of the root-cause stage; 0 when nothing failed
	Truncated bool   // the stage cap was hit; the tree is incomplete
	// Dropped holds the final snapshot of every stage whose end was never
	// handed to Emit (the queue was full, or the stage ended while Finish
	// was closing the queue). The handler resends them before the result,
	// so the client's tree is complete even when the stream fell behind.
	Dropped []StageSnapshot
}

// Options configure a trace for one test.
type Options struct {
	Fresh     bool                // the test runs with fresh=true (SPEC 139 §5.6)
	Probe     bool                // connections of this trace are probe connections (SPEC 139 §5.7)
	Depth     Depth               // how far the test goes
	MaxStages int                 // stage cap; 0 → DefaultMaxStages
	Emit      func(StageSnapshot) // receives every begin/end snapshot; may be nil
	// Outbounds lets Routed unfold nested selectors when the ctx the test
	// runs on carries no service registry (the gRPC stream ctx does not).
	Outbounds adapter.OutboundManager
}

// DefaultMaxStages is the stage cap of a test (SPEC 139 §5.15).
const DefaultMaxStages = 512

const emitQueueSize = 1024

// ErrResolveCycle is returned by BeginLookup when the same resolver/domain
// pair is already being resolved by an ancestor stage (SPEC 139 §5.5).
var ErrResolveCycle = errors.New("resolve cycle")

// Trace is the stage holder of one connection test.
type Trace struct {
	opts  Options
	start time.Time

	access     sync.Mutex
	stages     []*Stage
	nextID     uint32
	truncated  uint32
	finished   bool
	routed     bool // Routed wrote the route stage
	emitClosed bool // Finish closed emitQueue; emit must not send any more

	emitQueue chan StageSnapshot
	emitDone  chan struct{}
}

// Stage is one step of the pipeline recorded in a trace. A nil *Stage is a
// valid receiver for every method: insertion points never check for a trace.
type Stage struct {
	trace  *Trace
	id     uint32
	parent *Stage
	depth  int
	kind   string

	access     sync.Mutex
	subject    string
	start      time.Time
	startMs    uint32
	done       bool
	end        time.Time
	durationMs uint32
	result     Result
	err        string
	attrs      []Attr

	// lookup stages only: the resolver/domain pair for cycle detection.
	lookupResolver string
	lookupDomain   string

	// emittedEnd is set once the end snapshot was handed to the emitter.
	// Guarded by trace.access, not by access: emit and Finish hold that one.
	emittedEnd bool
}

type (
	traceKey struct{}
	stageKey struct{}
)

// New creates a trace. The emitter goroutine starts only when opts.Emit is
// set; Finish stops it.
func New(opts Options) *Trace {
	if opts.MaxStages <= 0 {
		opts.MaxStages = DefaultMaxStages
	}
	t := &Trace{
		opts:  opts,
		start: time.Now(),
	}
	if opts.Emit != nil {
		t.emitQueue = make(chan StageSnapshot, emitQueueSize)
		t.emitDone = make(chan struct{})
		go t.emitLoop()
	}
	return t
}

func (t *Trace) emitLoop() {
	defer close(t.emitDone)
	for snapshot := range t.emitQueue {
		t.opts.Emit(snapshot)
	}
}

// emit queues a snapshot for the emitter without blocking the pipeline: a
// full queue drops it. A dropped end snapshot is reported by Finish in
// Summary.Dropped so the handler can resend it; a dropped RUNNING snapshot is
// not recovered (the end snapshot carries the whole stage). The send happens
// under access together with the emitClosed check, so a stage that ends while
// Finish closes the queue can never send on the closed channel.
func (t *Trace) emit(s *Stage, snapshot StageSnapshot, final bool) {
	if t.emitQueue == nil {
		return
	}
	t.access.Lock()
	defer t.access.Unlock()
	if t.emitClosed {
		return
	}
	select {
	case t.emitQueue <- snapshot:
		if final && s != nil {
			s.emittedEnd = true
		}
	default:
	}
}

// With attaches the trace to ctx.
func With(ctx context.Context, t *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

// From returns the trace carried by ctx, nil when there is none.
func From(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

// Current returns the innermost stage carried by ctx, nil when there is none.
func Current(ctx context.Context) *Stage {
	s, _ := ctx.Value(stageKey{}).(*Stage)
	return s
}

// WithStage returns a ctx whose current stage is s, so that stages begun
// from it become children of s. It lets the handler attach synthesized
// children (proxy_header, skipped placeholders) to a stage it only knows by
// id. A nil s returns ctx unchanged.
func WithStage(ctx context.Context, s *Stage) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, stageKey{}, s)
}

// Stage returns the recorded stage with the given id, nil when unknown.
func (t *Trace) Stage(id uint32) *Stage {
	if t == nil || id == 0 {
		return nil
	}
	t.access.Lock()
	defer t.access.Unlock()
	if int(id) <= len(t.stages) && t.stages[id-1].id == id {
		return t.stages[id-1]
	}
	for _, s := range t.stages {
		if s.id == id {
			return s
		}
	}
	return nil
}

// Fresh reports whether the test in ctx runs with fresh=true.
func Fresh(ctx context.Context) bool {
	t := From(ctx)
	return t != nil && t.opts.Fresh
}

// IsProbe reports whether the connection in ctx belongs to a connection test.
func IsProbe(ctx context.Context) bool {
	t := From(ctx)
	return t != nil && t.opts.Probe
}

// DepthOf returns the depth of the test in ctx; DepthHTTP without a trace.
func DepthOf(ctx context.Context) Depth {
	t := From(ctx)
	if t == nil {
		return DepthHTTP
	}
	return t.opts.Depth
}

// Begin opens a stage as a child of the current stage of ctx and returns a
// child ctx that carries the new stage as the current one. Without a trace
// it returns (ctx, nil). Past the stage cap, or after Finish, it returns
// (ctx, nil) and counts the stage as truncated.
func Begin(ctx context.Context, kind, subject string) (context.Context, *Stage) {
	t := From(ctx)
	if t == nil {
		return ctx, nil
	}
	s := t.begin(Current(ctx), kind, subject)
	if s == nil {
		return ctx, nil
	}
	return context.WithValue(ctx, stageKey{}, s), s
}

func (t *Trace) begin(parent *Stage, kind, subject string) *Stage {
	now := time.Now()
	t.access.Lock()
	if t.finished || len(t.stages) >= t.opts.MaxStages {
		t.truncated++
		t.access.Unlock()
		return nil
	}
	t.nextID++
	s := &Stage{
		trace:   t,
		id:      t.nextID,
		parent:  parent,
		kind:    kind,
		subject: subject,
		start:   now,
		startMs: clampMs(now.Sub(t.start)),
		result:  Running,
	}
	if parent != nil {
		s.depth = parent.depth + 1
	}
	t.stages = append(t.stages, s)
	t.access.Unlock()
	t.emit(s, s.snapshot(), false)
	return s
}

// BeginLookup opens a `lookup` stage for resolving domain through resolver
// and checks the ancestors for the same pair: a repeat is a resolve cycle —
// the stage ends Failed with attr cycle_with=<ancestor id> and
// ErrResolveCycle is returned, so the caller fails at once instead of by
// timeout (SPEC 139 §5.5). Without a trace it returns (ctx, nil, nil).
func BeginLookup(ctx context.Context, resolver, domain string) (context.Context, *Stage, error) {
	t := From(ctx)
	if t == nil {
		return ctx, nil, nil
	}
	parent := Current(ctx)
	s := t.begin(parent, "lookup", domain)
	if s == nil {
		return ctx, nil, nil
	}
	s.lookupResolver = resolver
	s.lookupDomain = domain
	s.Attr("resolver", resolver)
	for ancestor := parent; ancestor != nil; ancestor = ancestor.parent {
		if ancestor.kind == "lookup" && ancestor.lookupResolver == resolver && ancestor.lookupDomain == domain {
			s.Attr("cycle_with", strconv.FormatUint(uint64(ancestor.id), 10))
			s.End(ErrResolveCycle)
			return ctx, s, ErrResolveCycle
		}
	}
	return context.WithValue(ctx, stageKey{}, s), s, nil
}

// ID returns the stage id, 0 on a nil stage.
func (s *Stage) ID() uint32 {
	if s == nil {
		return 0
	}
	return s.id
}

// Attr appends an attribute. Attributes keep call order.
func (s *Stage) Attr(key, value string) {
	if s == nil {
		return
	}
	s.access.Lock()
	s.attrs = append(s.attrs, Attr{Key: key, Value: value})
	s.access.Unlock()
}

// End closes the stage: nil → OK, otherwise Failed with the error text.
// The second call is ignored.
func (s *Stage) End(err error) {
	if s == nil {
		return
	}
	if err == nil {
		s.finish(OK, "")
		return
	}
	s.finish(Failed, err.Error())
}

// Mark closes the stage with a non-failure outcome that is not plain OK:
// Reused, NoAck, NotEvaluated or Skipped. The second close is ignored.
func (s *Stage) Mark(result Result) {
	if s == nil {
		return
	}
	s.finish(result, "")
}

// LostRace closes the stage as Skipped with lost_race=<winner id>: the stage
// was cancelled because a sibling won a parallel dial (SPEC 139 §5.13). It is
// never the root cause.
func (s *Stage) LostRace(winner *Stage) {
	if s == nil {
		return
	}
	s.Attr("lost_race", strconv.FormatUint(uint64(winner.ID()), 10))
	s.finish(Skipped, "")
}

// finish closes the stage once; later calls are ignored.
func (s *Stage) finish(result Result, errText string) {
	s.finishAt(result, errText, time.Now(), nil)
}

// finishAt is finish with an explicit end time (Finish closes every leftover
// stage with one shared time, so the root-cause tie-break by depth decides
// among them) and an optional attribute written together with the close.
func (s *Stage) finishAt(result Result, errText string, now time.Time, extra *Attr) {
	s.access.Lock()
	if s.done {
		s.access.Unlock()
		return
	}
	if extra != nil {
		s.attrs = append(s.attrs, *extra)
	}
	s.done = true
	s.end = now
	s.durationMs = clampMs(now.Sub(s.start))
	s.result = result
	s.err = errText
	snapshot := s.snapshotLocked()
	s.access.Unlock()
	s.trace.emit(s, snapshot, true)
}

// Children returns snapshots of the direct children of s, in begin order.
func (s *Stage) Children() []StageSnapshot {
	if s == nil {
		return nil
	}
	s.trace.access.Lock()
	defer s.trace.access.Unlock()
	var children []StageSnapshot
	for _, stage := range s.trace.stages {
		if stage.parent == s {
			children = append(children, stage.snapshot())
		}
	}
	return children
}

func (s *Stage) snapshot() StageSnapshot {
	s.access.Lock()
	defer s.access.Unlock()
	return s.snapshotLocked()
}

func (s *Stage) snapshotLocked() StageSnapshot {
	snapshot := StageSnapshot{
		ID:         s.id,
		Parent:     s.parent.ID(),
		Kind:       s.kind,
		Subject:    s.subject,
		StartMs:    s.startMs,
		DurationMs: s.durationMs,
		Result:     s.result,
		Error:      s.err,
	}
	if len(s.attrs) > 0 {
		snapshot.Attrs = make([]Attr, len(s.attrs))
		copy(snapshot.Attrs, s.attrs)
	}
	return snapshot
}

func (s *Stage) hasAttr(key string) bool {
	for _, attr := range s.attrs {
		if attr.Key == key {
			return true
		}
	}
	return false
}

// Finish closes the trace and returns the summary with the root cause
// (SPEC 139 §5.2). Every still-running stage is closed with one shared end
// time: with a cause (timeout, cancel) it ends Failed with that cause, so the
// deepest unfinished stage becomes the root (§5.11); with a nil cause the
// path completed and leftovers end Skipped with reason=unfinished, never as
// a false root. The emitter is drained and stopped; later Begin calls are
// no-ops. Finish is idempotent; repeated calls return the same summary.
func (t *Trace) Finish(cause error) Summary {
	now := time.Now()
	t.access.Lock()
	alreadyFinished := t.finished
	t.finished = true
	stages := make([]*Stage, len(t.stages))
	copy(stages, t.stages)
	truncated := t.truncated > 0
	t.access.Unlock()

	if !alreadyFinished {
		if cause != nil {
			causeText := cause.Error()
			for _, s := range stages {
				s.finishAt(Failed, causeText, now, nil)
			}
		} else {
			unfinished := Attr{Key: "reason", Value: "unfinished"}
			for _, s := range stages {
				s.finishAt(Skipped, "", now, &unfinished)
			}
		}
		if t.emitQueue != nil {
			t.access.Lock()
			t.emitClosed = true
			close(t.emitQueue)
			t.access.Unlock()
			<-t.emitDone
		}
	}

	summary := Summary{
		Stages:    make([]StageSnapshot, 0, len(stages)),
		Truncated: truncated,
	}
	for _, s := range stages {
		summary.Stages = append(summary.Stages, s.snapshot())
	}
	if t.emitQueue != nil {
		t.access.Lock()
		for _, s := range stages {
			if !s.emittedEnd {
				summary.Dropped = append(summary.Dropped, s.snapshot())
			}
		}
		t.access.Unlock()
	}
	summary.RootCause = rootCause(stages)
	return summary
}

// RouteRecorded reports whether Routed has written the route stage of this
// trace. The handler reads it synchronously, so a fast first-hop failure
// cannot make it add a second route stage of its own.
func (t *Trace) RouteRecorded() bool {
	if t == nil {
		return false
	}
	t.access.Lock()
	defer t.access.Unlock()
	return t.routed
}

// rootCause implements SPEC 139 §5.2: among Failed stages that did not lose a
// race, the one that finished first and has no Failed descendant. Ties by end
// time go to the deeper stage, then to the smaller id.
func rootCause(stages []*Stage) uint32 {
	failed := make(map[uint32]bool, len(stages))
	for _, s := range stages {
		s.access.Lock()
		if s.result == Failed && !s.hasAttr("lost_race") {
			failed[s.id] = true
		}
		s.access.Unlock()
	}
	if len(failed) == 0 {
		return 0
	}
	// A stage has a failed descendant iff some failed stage has it among its ancestors.
	hasFailedDescendant := make(map[uint32]bool, len(failed))
	for _, s := range stages {
		if !failed[s.id] {
			continue
		}
		for ancestor := s.parent; ancestor != nil; ancestor = ancestor.parent {
			hasFailedDescendant[ancestor.id] = true
		}
	}
	candidates := make([]*Stage, 0, len(failed))
	for _, s := range stages {
		if failed[s.id] && !hasFailedDescendant[s.id] {
			candidates = append(candidates, s)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.end.Equal(b.end) {
			return a.end.Before(b.end)
		}
		if a.depth != b.depth {
			return a.depth > b.depth
		}
		return a.id < b.id
	})
	return candidates[0].id
}

// Routed records the route decision as a `route` stage (SPEC 139 §4) and
// reports whether the test stops here (DepthRoute / DepthDNS). It is the
// single insertion point in route/route.go, placed before the trackers, so
// a DepthRoute test opens no connection and never shows in the connection
// list. Without a trace it returns false after one ctx.Value lookup.
func Routed(ctx context.Context, metadata *adapter.InboundContext, rule adapter.Rule, outbound adapter.Outbound) bool {
	t := From(ctx)
	if t == nil {
		return false
	}
	_, s := Begin(ctx, "route", metadata.Destination.String())
	if s != nil {
		t.access.Lock()
		t.routed = true
		t.access.Unlock()
		s.Attr("network", metadata.Network)
		if rule != nil {
			s.Attr("rule", rule.String())
			if action := rule.Action(); action != nil {
				s.Attr("action", action.String())
			}
		} else {
			s.Attr("rule", "final")
			s.Attr("action", "route")
		}
		if outbound != nil {
			s.Attr("outbound", outbound.Tag())
			s.Attr("outbound_type", outbound.Type())
		}
		s.Attr("domain_origin", domainOrigin(metadata))
		if metadata.Protocol != "" {
			s.Attr("protocol", metadata.Protocol)
		}
		if metadata.Domain != "" {
			s.Attr("domain", metadata.Domain)
		}
		if metadata.Client != "" {
			s.Attr("client", metadata.Client)
		}
		if metadata.OriginDestination.IsValid() {
			s.Attr("original_destination", metadata.OriginDestination.String())
		}
		if len(metadata.DestinationAddresses) > 0 {
			addresses := make([]string, 0, len(metadata.DestinationAddresses))
			for _, address := range metadata.DestinationAddresses {
				addresses = append(addresses, address.String())
			}
			s.Attr("addresses", strings.Join(addresses, ","))
		}
		if metadata.Inbound != "" {
			s.Attr("inbound", metadata.Inbound)
		}
		if metadata.Source.IsValid() {
			s.Attr("source", metadata.Source.String())
		}
		if metadata.ProcessInfo != nil {
			if process := processName(metadata.ProcessInfo); process != "" {
				s.Attr("process", process)
			}
			if metadata.ProcessInfo.UserId != -1 {
				s.Attr("user_id", strconv.FormatInt(int64(metadata.ProcessInfo.UserId), 10))
			}
		}
		s.End(nil)
		unfoldSelectors(ctx, t, outbound)
	}
	switch t.opts.Depth {
	case DepthRoute, DepthDNS:
		return true
	}
	return false
}

func domainOrigin(metadata *adapter.InboundContext) string {
	switch {
	case metadata.FakeIP:
		return "fakeip"
	case metadata.Protocol != "" && metadata.Domain != "":
		return "sniff"
	case metadata.Domain != "":
		return "reverse_mapping"
	case metadata.Destination.IsFqdn():
		return "request"
	}
	return "none"
}

func processName(info *adapter.ConnectionOwner) string {
	if info.ProcessPath != "" {
		return info.ProcessPath
	}
	if len(info.AndroidPackageNames) > 0 {
		return info.AndroidPackageNames[0]
	}
	return ""
}

// unfoldSelectors writes one `group` stage per nested selector, following
// Now() through the outbound manager until the first non-selector. Other
// group types (urltest, failover) record their per-connection choice in
// their own lx code, so they are not synthesized here.
// OutboundsOf returns the outbound manager a stage writer may use to follow a
// group to its node: the one registered in ctx, else the one the test handler
// passed in Options (the stream ctx carries no service registry). nil without
// a trace or when neither is known.
func OutboundsOf(ctx context.Context) adapter.OutboundManager {
	if manager := service.FromContext[adapter.OutboundManager](ctx); manager != nil {
		return manager
	}
	if t := From(ctx); t != nil {
		return t.opts.Outbounds
	}
	return nil
}

func unfoldSelectors(ctx context.Context, t *Trace, outbound adapter.Outbound) {
	manager := service.FromContext[adapter.OutboundManager](ctx)
	if manager == nil {
		manager = t.opts.Outbounds
	}
	seen := make(map[string]bool)
	for outbound != nil && outbound.Type() == C.TypeSelector {
		group, isGroup := outbound.(adapter.OutboundGroup)
		if !isGroup || seen[outbound.Tag()] {
			return
		}
		seen[outbound.Tag()] = true
		selected := adapter.GroupNow(group)
		_, s := Begin(ctx, "group", outbound.Tag())
		s.Attr("type", outbound.Type())
		s.Attr("selected", selected)
		s.End(nil)
		if manager == nil {
			return
		}
		next, loaded := manager.Outbound(selected)
		if !loaded {
			return
		}
		outbound = next
	}
}

func clampMs(d time.Duration) uint32 {
	ms := d.Milliseconds()
	if ms < 0 {
		return 0
	}
	if ms > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ms)
}
