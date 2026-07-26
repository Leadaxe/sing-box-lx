// Package group implements the lx `group` DNS server type (SPEC 033 v2):
// several member servers behind one tag with a selection strategy built on a
// TTL record model. Servers have NO states (no down/backoff); there are two
// tables of expiring records instead:
//
//   - an ERROR record (error_ttl) is written by any failed exchange and
//     erases the server's live wins;
//   - a WIN record (win_ttl, fastest only) is written ONLY by the first
//     successful answer of a fan-out (competitive — otherwise the current
//     server would self-reinforce), and any successful exchange erases the
//     server's live errors (which is NOT a win — no self-reinforcement).
//
// CLEAN = zero live errors. Modes select a target among the clean; with no
// clean member every query makes exactly ONE attempt via the least dirty
// server and never fans (anti-storm on a dead network — "survival mode").
// A network change (Reset) amnesties both tables.
//
// The group sits UNDER the DNS cache (cache keys carry the group tag):
// only the answer the group returns is cached; fan answers it discards
// never reach the cache.
//
// Observability (SPEC 035): probes are recorded into the per-request query
// trace (write-once effective, group path, attempts, fanned/survival flags);
// GroupState() exposes the record snapshot for GetDNSGroups.
package group

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dnstrack"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

const (
	ModeStable   = "stable"
	ModeFastest  = "fastest"
	ModeParallel = "parallel"

	DefaultErrorTTL = 2 * time.Minute
	DefaultWinTTL   = 5 * time.Minute

	// maxRecords caps each record slice: counts above it are
	// indistinguishable for selection, and the cap bounds memory on a dead
	// network where errors accumulate with every query.
	maxRecords = 64
)

func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.GroupDNSServerOptions](registry, C.DNSTypeGroup, NewTransport)
}

var _ adapter.DNSTransport = (*Transport)(nil)

type member struct {
	tag       string
	transport adapter.DNSTransport
}

// memberRecord is the per-member TTL record pair. Guarded by
// Transport.access; slices are pruned lazily on read.
type memberRecord struct {
	errors  []time.Time
	wins    []time.Time
	lastRTT time.Duration // last successful probe; 0 = never measured
}

type Transport struct {
	dns.TransportAdapter
	ctx        context.Context
	logger     log.ContextLogger
	serverTags []string
	mode       string
	errorTTL   time.Duration
	winTTL     time.Duration

	access   sync.Mutex
	members  []*member
	records  map[string]*memberRecord
	current  string // sticky target (stable/fastest); "" = not chosen yet
	election bool   // fastest: an election fan is in flight (single-flight)
	gen      int    // bumped by Reset; a finishing fan from an older gen drops state writes
}

func NewTransport(ctx context.Context, logger log.ContextLogger, tag string, options option.GroupDNSServerOptions) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, E.New("group[", tag, "]: servers is required and must not be empty")
	}
	seen := make(map[string]bool, len(options.Servers))
	for _, serverTag := range options.Servers {
		if serverTag == tag {
			return nil, E.New("group[", tag, "]: group cannot contain itself")
		}
		if seen[serverTag] {
			return nil, E.New("group[", tag, "]: duplicate server: ", serverTag)
		}
		seen[serverTag] = true
	}
	mode := options.Mode
	switch mode {
	case "":
		mode = ModeStable
	case ModeStable, ModeFastest, ModeParallel:
	default:
		return nil, E.New("group[", tag, "]: unknown mode: ", mode, " (expected ", ModeStable, ", ", ModeFastest, " or ", ModeParallel, ")")
	}
	winTTL := time.Duration(options.WinTTL)
	if mode != ModeFastest && winTTL != 0 {
		logger.Warn("group[", tag, "]: win_ttl is only used in fastest mode, ignoring")
		winTTL = 0
	}
	if winTTL <= 0 {
		winTTL = DefaultWinTTL
	}
	errorTTL := time.Duration(options.ErrorTTL)
	if errorTTL <= 0 {
		errorTTL = DefaultErrorTTL
	}
	return &Transport{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeGroup, tag, options.Servers),
		ctx:              ctx,
		logger:           logger,
		serverTags:       options.Servers,
		mode:             mode,
		errorTTL:         errorTTL,
		winTTL:           winTTL,
		records:          make(map[string]*memberRecord),
	}, nil
}

func (t *Transport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	transportManager := service.FromContext[adapter.DNSTransportManager](t.ctx)
	if transportManager == nil {
		return E.New("group[", t.Tag(), "]: missing DNS transport manager")
	}
	for _, serverTag := range t.serverTags {
		rawTransport, loaded := transportManager.Transport(serverTag)
		if !loaded {
			return E.New("group[", t.Tag(), "]: DNS server not found: ", serverTag)
		}
		switch rawTransport.Type() {
		case C.DNSTypeFakeIP, C.DNSTypeHosts:
			// Local sources: they cannot fail over the network, so backing
			// them up is meaningless and their presence is a config mistake.
			return E.New("group[", t.Tag(), "]: server type ", rawTransport.Type(), " is not allowed in a group: ", serverTag)
		}
		t.members = append(t.members, &member{tag: serverTag, transport: rawTransport})
	}
	return nil
}

func (t *Transport) Close() error {
	return nil
}

// Reset is the network-change amnesty: both record tables, the sticky
// target and the election generation drop. Members are reset by the
// manager's own loop; the group must not fan Reset out.
func (t *Transport) Reset() {
	t.access.Lock()
	defer t.access.Unlock()
	t.records = make(map[string]*memberRecord)
	t.current = ""
	t.gen++
	// `election` is left as-is: an in-flight fan still owns the flag and
	// clears it on completion; its state writes are dropped by the gen check.
}

// --- record operations (all under access) -----------------------------------

func (t *Transport) recordLocked(tag string) *memberRecord {
	record := t.records[tag]
	if record == nil {
		record = &memberRecord{}
		t.records[tag] = record
	}
	return record
}

func pruneTimes(times []time.Time, ttl time.Duration, now time.Time) []time.Time {
	firstLive := len(times)
	for i, at := range times {
		if now.Sub(at) < ttl {
			firstLive = i
			break
		}
	}
	return times[firstLive:]
}

func appendCapped(times []time.Time, at time.Time) []time.Time {
	times = append(times, at)
	if len(times) > maxRecords {
		times = times[len(times)-maxRecords:]
	}
	return times
}

func (t *Transport) liveErrorsLocked(tag string, now time.Time) []time.Time {
	record := t.records[tag]
	if record == nil {
		return nil
	}
	record.errors = pruneTimes(record.errors, t.errorTTL, now)
	return record.errors
}

func (t *Transport) liveWinsLocked(tag string, now time.Time) []time.Time {
	record := t.records[tag]
	if record == nil {
		return nil
	}
	record.wins = pruneTimes(record.wins, t.winTTL, now)
	return record.wins
}

// noteError records a failed exchange: an error record is written and the
// server's live wins are erased (a win must not outlive a failure — that is
// the flap-back trap of win_ttl > error_ttl). gen-guarded: a probe started
// before Reset must not poison the amnestied tables.
func (t *Transport) noteError(tag string, gen int) {
	t.access.Lock()
	defer t.access.Unlock()
	if t.gen != gen {
		return
	}
	record := t.recordLocked(tag)
	record.errors = appendCapped(record.errors, time.Now())
	record.wins = nil
}

// noteSuccess records a successful exchange: the server's live errors are
// erased. This is NOT a win — erasing errors returns the server to the clean
// set but gives it no advantage inside it (no self-reinforcement).
// gen-guarded like noteError: a stale success must not erase legitimate
// post-Reset errors.
func (t *Transport) noteSuccess(tag string, rtt time.Duration, gen int) {
	t.access.Lock()
	defer t.access.Unlock()
	if t.gen != gen {
		return
	}
	record := t.recordLocked(tag)
	record.errors = nil
	if rtt > 0 {
		record.lastRTT = rtt
	}
}

// noteWin records a competitive win (first success of a fan). fastest only —
// no other mode reads wins.
func (t *Transport) noteWin(tag string, gen int) {
	t.access.Lock()
	defer t.access.Unlock()
	if t.gen != gen {
		return
	}
	record := t.recordLocked(tag)
	record.wins = appendCapped(record.wins, time.Now())
}

// setCurrent updates the sticky target; reports the previous value when it
// actually changed (for the info log).
func (t *Transport) setCurrent(tag string, gen int) (previous string, changed bool) {
	t.access.Lock()
	defer t.access.Unlock()
	if t.gen != gen || t.current == tag {
		return "", false
	}
	previous = t.current
	t.current = tag
	return previous, true
}

// --- target selection --------------------------------------------------------

// selection is the routing decision for one query.
type selection struct {
	target      *member   // single-exchange target (nil when fan is set)
	fan         []*member // fan participants (parallel / fastest election)
	election    bool      // this query holds the fastest election flag
	survival    bool      // no clean members: one attempt, no fan
	provisional bool      // election-window concurrent: must NOT re-elect current
	gen         int
}

func (t *Transport) selectTarget() selection {
	now := time.Now()
	t.access.Lock()
	defer t.access.Unlock()

	var clean []*member
	for _, current := range t.members {
		if len(t.liveErrorsLocked(current.tag, now)) == 0 {
			clean = append(clean, current)
		}
	}

	if len(clean) == 0 {
		return selection{target: t.leastDirtyLocked(now), survival: true, gen: t.gen}
	}

	switch t.mode {
	case ModeParallel:
		return selection{fan: append([]*member(nil), clean...), gen: t.gen}
	case ModeFastest:
		best, maxWins := t.fastestCandidatesLocked(clean, now)
		if maxWins > 0 {
			return selection{target: t.stickyPickLocked(best), gen: t.gen}
		}
		// Nobody has a live win — election time. Single-flight: exactly one
		// query fans; concurrents of the window go to a random clean member.
		if !t.election {
			t.election = true
			return selection{fan: append([]*member(nil), clean...), election: true, gen: t.gen}
		}
		return selection{target: clean[rand.IntN(len(clean))], provisional: true, gen: t.gen}
	default: // ModeStable
		return selection{target: t.stickyPickLocked(clean), gen: t.gen}
	}
}

// stickyPickLocked implements «липкость прежде случайности»: keep the
// current target while it belongs to the candidate set; re-elect a random
// candidate otherwise.
func (t *Transport) stickyPickLocked(candidates []*member) *member {
	if t.current != "" {
		for _, candidate := range candidates {
			if candidate.tag == t.current {
				return candidate
			}
		}
	}
	return candidates[rand.IntN(len(candidates))]
}

// fastestCandidatesLocked returns the clean members carrying the maximum
// number of live wins, and that maximum.
func (t *Transport) fastestCandidatesLocked(clean []*member, now time.Time) ([]*member, int) {
	maxWins := 0
	var best []*member
	for _, candidate := range clean {
		wins := len(t.liveWinsLocked(candidate.tag, now))
		switch {
		case wins > maxWins:
			maxWins = wins
			best = best[:0]
			best = append(best, candidate)
		case wins == maxWins:
			best = append(best, candidate)
		}
	}
	return best, maxWins
}

// leastDirtyLocked picks the survival target: fewest live errors, tie broken
// by the OLDEST last error, full tie — randomly.
func (t *Transport) leastDirtyLocked(now time.Time) *member {
	var (
		best      []*member
		bestCount = -1
		bestLast  time.Time
	)
	for _, candidate := range t.members {
		errs := t.liveErrorsLocked(candidate.tag, now)
		count := len(errs)
		var last time.Time
		if count > 0 {
			last = errs[count-1]
		}
		switch {
		case bestCount == -1 || count < bestCount || (count == bestCount && last.Before(bestLast)):
			best = best[:0]
			best = append(best, candidate)
			bestCount = count
			bestLast = last
		case count == bestCount && last.Equal(bestLast):
			best = append(best, candidate)
		}
	}
	return best[rand.IntN(len(best))]
}

// --- exchange ----------------------------------------------------------------

func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	dnstrack.PushGroup(ctx, t.Tag()) // prepend: final order is inside-out
	sel := t.selectTarget()

	if sel.fan != nil {
		return t.fan(ctx, message, sel.fan, sel.gen, sel.election)
	}
	if sel.survival {
		return t.exchangeSurvival(ctx, message, sel)
	}
	return t.exchangeSingle(ctx, message, sel)
}

func (t *Transport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(t.Exchange(ctx, message))
	}()
}

// exchangeSingle is the normal path: one exchange with the sticky/best
// target under a sub-deadline of HALF the remaining budget — the other half
// is guaranteed to the rescue fan (otherwise a blackholed target eats the
// whole budget and the fan starts dead).
func (t *Transport) exchangeSingle(ctx context.Context, message *mDNS.Msg, sel selection) (*mDNS.Msg, error) {
	// An election-window concurrent serves the query but must not trash the
	// sticky target (nor spam the info log): current changes only via the
	// sticky pick or the fan winner.
	if !sel.provisional {
		if previous, changed := t.setCurrent(sel.target.tag, sel.gen); changed {
			t.logCurrentChange(ctx, previous, sel.target.tag)
		}
	}
	targetCtx, cancel := context.WithTimeout(ctx, t.targetBudget(ctx))
	response, err, rtt := t.timedExchange(targetCtx, sel.target, message)
	cancel()
	t.traceAttempt(ctx, sel.target, response, err, rtt)
	if !isFailure(response, err) {
		t.noteSuccess(sel.target.tag, rtt, sel.gen)
		t.recordEffective(ctx, sel.target)
		return response, err
	}
	// The target's sub-deadline was honest — its failure is always recorded.
	t.noteError(sel.target.tag, sel.gen)
	t.logProbeFailure(ctx, sel.target.tag, response, err)

	rescuers := t.cleanExcept(sel.target.tag)
	if len(rescuers) == 0 {
		return response, err
	}
	return t.fan(ctx, message, rescuers, sel.gen, false)
}

// exchangeSurvival: no clean members — exactly one attempt via the least
// dirty server, never a fan (anti-storm: one attempt is the whole price of
// the query on a dead network). The attempt gets the FULL remaining budget:
// there is no fan to reserve for.
func (t *Transport) exchangeSurvival(ctx context.Context, message *mDNS.Msg, sel selection) (*mDNS.Msg, error) {
	target := sel.target
	dnstrack.MarkSurvival(ctx)
	t.logger.WarnContext(ctx, "group[", t.Tag(), "]: no clean servers, survival attempt via ", target.tag)
	response, err, rtt := t.timedExchange(ctx, target, message)
	t.traceAttempt(ctx, target, response, err, rtt)
	if !isFailure(response, err) {
		t.noteSuccess(target.tag, rtt, sel.gen) // erases its errors → back to clean, stickiness holds it
		t.recordEffective(ctx, target)
		return response, err
	}
	t.noteError(target.tag, sel.gen)
	t.logProbeFailure(ctx, target.tag, response, err)
	return response, err
}

// targetBudget is half of the remaining request budget (fallback: half of
// the client default when the context carries no deadline).
func (t *Transport) targetBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining / 2
		}
	}
	return C.DNSTimeout / 2
}

func (t *Transport) timedExchange(ctx context.Context, current *member, message *mDNS.Msg) (*mDNS.Msg, error, time.Duration) {
	started := time.Now()
	response, err := current.transport.Exchange(ctx, message)
	return response, err, time.Since(started)
}

// cleanExcept returns the clean members minus the given tag.
func (t *Transport) cleanExcept(exceptTag string) []*member {
	now := time.Now()
	t.access.Lock()
	defer t.access.Unlock()
	var clean []*member
	for _, current := range t.members {
		if current.tag == exceptTag {
			continue
		}
		if len(t.liveErrorsLocked(current.tag, now)) == 0 {
			clean = append(clean, current)
		}
	}
	return clean
}

// --- classification / trace / log -------------------------------------------

// recordEffective attributes the answer to the member that produced it for
// the DNS query stream. Write-once inside the holder, so with nesting the
// innermost leaf wins. No-op without the client-level holder.
func (t *Transport) recordEffective(ctx context.Context, current *member) {
	dnstrack.SetEffectiveServer(ctx, current.tag, current.transport.Type(), current.transport.OutboundTag())
}

// traceAttempt records one resolved probe into the query trace. Leaves only:
// an inner group's own members already tell its story.
func (t *Transport) traceAttempt(ctx context.Context, current *member, response *mDNS.Msg, err error, rtt time.Duration) {
	if current.transport.Type() == C.DNSTypeGroup {
		return
	}
	dnstrack.RecordAttempt(ctx, dnstrack.Attempt{
		Server:     current.tag,
		ServerType: current.transport.Type(),
		Outcome:    outcomeOf(response, err),
		RTTMs:      uint32(rtt.Milliseconds()),
	})
}

// isFailure classifies a member result per the feature contract: transport
// errors, timeouts and SERVFAIL are failures; NXDOMAIN and empty responses
// are valid answers. Members surface rcodes both as *mDNS.Msg and as
// dns.RcodeError (the client converts the error form above the group), so
// both representations are checked.
func isFailure(response *mDNS.Msg, err error) bool {
	if err != nil {
		var rcode dns.RcodeError
		if errors.As(err, &rcode) {
			return rcode == dns.RcodeServerFailure
		}
		return true
	}
	return response != nil && response.Rcode == mDNS.RcodeServerFailure
}

// outcomeOf maps a probe result onto the wire vocabulary of the query trace:
// answered | timeout | network_error | servfail.
func outcomeOf(response *mDNS.Msg, err error) string {
	if !isFailure(response, err) {
		return dnstrack.AttemptAnswered
	}
	if err != nil {
		var rcode dns.RcodeError
		if errors.As(err, &rcode) && rcode == dns.RcodeServerFailure {
			return dnstrack.AttemptServfail
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
			return dnstrack.AttemptTimeout
		}
		return dnstrack.AttemptNetworkError
	}
	return dnstrack.AttemptServfail
}

func (t *Transport) logProbeFailure(ctx context.Context, tag string, response *mDNS.Msg, err error) {
	if err == nil {
		err = dns.RcodeError(response.Rcode)
	}
	t.logger.DebugContext(ctx, "group[", t.Tag(), "]: server ", tag, " error recorded (ttl ", t.errorTTL.String(), "): ", err)
}

func (t *Transport) logCurrentChange(ctx context.Context, previous string, next string) {
	if previous == "" {
		t.logger.InfoContext(ctx, "group[", t.Tag(), "]: current server: ", next)
		return
	}
	t.logger.InfoContext(ctx, "group[", t.Tag(), "]: current server changed ", previous, " -> ", next)
}

// --- GetDNSGroups state snapshot ---------------------------------------------

// MemberState is the health snapshot of one member for the state RPC.
type MemberState struct {
	Tag          string
	ServerType   string
	Clean        bool
	LiveErrors   int
	LastErrorAge time.Duration // valid when HasError
	HasError     bool
	LiveWins     int
	Current      bool
	LastRTT      time.Duration // 0 = never measured
}

// State is the group snapshot for the GetDNSGroups RPC.
type State struct {
	Tag     string
	Mode    string
	Current string // "" = not chosen yet / parallel
	Members []MemberState
}

// GroupState returns a point-in-time record snapshot. The daemon handler
// discovers groups by asserting this method on the manager's transports.
func (t *Transport) GroupState() State {
	now := time.Now()
	t.access.Lock()
	defer t.access.Unlock()
	snapshot := State{
		Tag:     t.Tag(),
		Mode:    t.mode,
		Current: t.current,
	}
	for _, current := range t.members {
		errs := t.liveErrorsLocked(current.tag, now)
		memberSnapshot := MemberState{
			Tag:        current.tag,
			ServerType: current.transport.Type(),
			Clean:      len(errs) == 0,
			LiveErrors: len(errs),
			LiveWins:   len(t.liveWinsLocked(current.tag, now)),
			Current:    current.tag == t.current,
		}
		if len(errs) > 0 {
			memberSnapshot.HasError = true
			memberSnapshot.LastErrorAge = now.Sub(errs[len(errs)-1])
		}
		if record := t.records[current.tag]; record != nil {
			memberSnapshot.LastRTT = record.lastRTT
		}
		snapshot.Members = append(snapshot.Members, memberSnapshot)
	}
	return snapshot
}
