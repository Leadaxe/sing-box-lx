// Package group implements the lx `group` DNS server type (SPEC 033/034/035):
// several member servers behind one tag with a selection strategy. Failover
// walks the member list in order, skipping servers inside their down_time
// window; race (race.go) periodically fans a real query out to all members
// and pins the fastest responder until the next race.
//
// The group sits UNDER the DNS cache (cache keys carry the group tag), so
// whatever the group returns is cached once for the whole group; member
// responses that the group discards never reach the cache.
//
// Observability (SPEC 035): every resolved probe is recorded into the
// per-request query trace (common/dnstrack) — the answering member
// (write-once, so nested groups attribute the leaf), the group path, the
// probe chronology with outcome and rtt. GroupState() exposes the health
// snapshot for the GetDNSGroups RPC.
package group

import (
	"context"
	"errors"
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
	ModeFailover = "failover"
	ModeRace     = "race"

	DefaultDownTime = 30 * time.Second
	DefaultInterval = 3 * time.Minute
)

func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.GroupDNSServerOptions](registry, C.DNSTypeGroup, NewTransport)
}

var _ adapter.DNSTransport = (*Transport)(nil)

type member struct {
	tag       string
	transport adapter.DNSTransport
}

// memberState is the per-member health memory (SPEC 033/036). Guarded by
// Transport.access. lastFail zero = the member is up.
type memberState struct {
	lastFail    time.Time
	consecFails int
	lastRTT     time.Duration // last successful probe; 0 = never measured
}

type Transport struct {
	dns.TransportAdapter
	ctx        context.Context
	logger     log.ContextLogger
	serverTags []string
	mode       string
	interval   time.Duration
	downTime   time.Duration

	access  sync.Mutex
	members []*member
	state   map[string]*memberState

	race raceState // SPEC 034; unused in failover mode
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
		mode = ModeFailover
	case ModeFailover, ModeRace:
	default:
		return nil, E.New("group[", tag, "]: unknown mode: ", mode, " (expected ", ModeFailover, " or ", ModeRace, ")")
	}
	interval := time.Duration(options.Interval)
	if mode == ModeFailover && interval != 0 {
		logger.Warn("group[", tag, "]: interval is only used in race mode, ignoring")
		interval = 0
	}
	if interval == 0 {
		interval = DefaultInterval
	}
	downTime := time.Duration(options.DownTime)
	if downTime <= 0 {
		downTime = DefaultDownTime
	}
	return &Transport{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeGroup, tag, options.Servers),
		ctx:              ctx,
		logger:           logger,
		serverTags:       options.Servers,
		mode:             mode,
		interval:         interval,
		downTime:         downTime,
		state:            make(map[string]*memberState),
		race:             raceState{firstDone: make(chan struct{})},
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

// Reset drops health state: down marks, per-member counters and (in race
// mode) the winner and ranking. A network change invalidates all of it.
// Members are reset by the manager's own loop; the group must not fan Reset
// out.
func (t *Transport) Reset() {
	t.access.Lock()
	defer t.access.Unlock()
	t.state = make(map[string]*memberState)
	t.race.reset()
}

func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	dnstrack.PushGroup(ctx, t.Tag()) // SPEC 035 — prepend: final order is inside-out
	if t.mode == ModeRace {
		return t.exchangeRace(ctx, message)
	}
	return t.exchangeFailover(ctx, message)
}

func (t *Transport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(t.Exchange(ctx, message))
	}()
}

// exchangeFailover walks members in list order, skipping the ones inside
// their down_time window. One failure marks a member down; when every member
// is down, exactly one attempt goes to the member whose failure is the
// oldest — a miss refreshes its mark, so consecutive queries rotate.
func (t *Transport) exchangeFailover(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	return t.exchangeOrdered(ctx, message, t.aliveMembers())
}

// exchangeLastResort handles the all-down case: one attempt via the member
// with the oldest failure mark.
func (t *Transport) exchangeLastResort(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	current := t.oldestFailedMember()
	t.logger.WarnContext(ctx, "group[", t.Tag(), "]: all servers down, last-resort attempt via ", current.tag)
	response, err := t.probeMember(ctx, current, message)
	if isFailure(response, err) {
		return response, err
	}
	t.recordEffective(ctx, current)
	return response, err
}

// probeMember performs one member exchange and settles its bookkeeping.
func (t *Transport) probeMember(ctx context.Context, current *member, message *mDNS.Msg) (*mDNS.Msg, error) {
	started := time.Now()
	response, err := current.transport.Exchange(ctx, message)
	t.finishProbe(ctx, current, response, err, time.Since(started))
	return response, err
}

// finishProbe updates health state and the query trace for one resolved
// probe. Shared by the sequential walks and the race collector. Returns
// whether the probe was a failure.
func (t *Transport) finishProbe(ctx context.Context, current *member, response *mDNS.Msg, err error, rtt time.Duration) bool {
	failure := isFailure(response, err)
	if failure {
		t.markFailure(current.tag)
		t.logFailure(ctx, current.tag, response, err)
	} else {
		t.recordSuccess(current.tag, rtt)
	}
	// Leaves only: an inner group's own members already tell its story —
	// a `group`-typed attempt would duplicate their time under one row.
	if current.transport.Type() != C.DNSTypeGroup {
		dnstrack.RecordAttempt(ctx, dnstrack.Attempt{
			Server:     current.tag,
			ServerType: current.transport.Type(),
			Outcome:    outcomeOf(response, err),
			RTTMs:      uint32(rtt.Milliseconds()),
		})
	}
	return failure
}

// recordEffective attributes the answer to the member that produced it for
// the DNS query stream (SPEC 035). Write-once inside the holder, so with
// nesting the innermost leaf wins. No-op without the client-level holder.
func (t *Transport) recordEffective(ctx context.Context, current *member) {
	dnstrack.SetEffectiveServer(ctx, current.tag, current.transport.Type(), current.transport.OutboundTag())
}

// isFailure classifies a member result per the feature contract: transport
// errors, timeouts and SERVFAIL fail over; NXDOMAIN and empty responses are
// valid answers. Members surface rcodes both as *mDNS.Msg and as
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

// outcomeOf maps a probe result onto the wire vocabulary of the query trace
// (SPEC 035): answered | timeout | network_error | servfail.
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

func (t *Transport) logFailure(ctx context.Context, tag string, response *mDNS.Msg, err error) {
	if err == nil {
		err = dns.RcodeError(response.Rcode)
	}
	t.logger.DebugContext(ctx, "group[", t.Tag(), "]: server ", tag, " down for ", t.downTime.String(), ": ", err)
}

func (t *Transport) aliveMembers() []*member {
	t.access.Lock()
	defer t.access.Unlock()
	return t.aliveMembersLocked()
}

func (t *Transport) aliveMembersLocked() []*member {
	now := time.Now()
	var alive []*member
	for _, current := range t.members {
		if state := t.state[current.tag]; state == nil || state.lastFail.IsZero() || now.Sub(state.lastFail) >= t.downTime {
			alive = append(alive, current)
		}
	}
	return alive
}

func (t *Transport) oldestFailedMember() *member {
	t.access.Lock()
	defer t.access.Unlock()
	oldest := t.members[0]
	oldestAt := t.failedAtLocked(oldest.tag)
	for _, current := range t.members[1:] {
		if failedAt := t.failedAtLocked(current.tag); failedAt.Before(oldestAt) {
			oldest, oldestAt = current, failedAt
		}
	}
	return oldest
}

func (t *Transport) failedAtLocked(tag string) time.Time {
	if state := t.state[tag]; state != nil {
		return state.lastFail
	}
	return time.Time{}
}

func (t *Transport) stateOfLocked(tag string) *memberState {
	state := t.state[tag]
	if state == nil {
		state = &memberState{}
		t.state[tag] = state
	}
	return state
}

func (t *Transport) markFailure(tag string) {
	t.access.Lock()
	defer t.access.Unlock()
	state := t.stateOfLocked(tag)
	state.lastFail = time.Now()
	state.consecFails++
}

func (t *Transport) recordSuccess(tag string, rtt time.Duration) {
	t.access.Lock()
	defer t.access.Unlock()
	state := t.stateOfLocked(tag)
	state.lastFail = time.Time{}
	state.consecFails = 0
	state.lastRTT = rtt
}

// --- GetDNSGroups state snapshot (SPEC 035) ---------------------------------

// MemberState is the health snapshot of one member for the state RPC.
type MemberState struct {
	Tag                 string
	ServerType          string
	Up                  bool
	DownRemaining       time.Duration // 0 when up
	ConsecutiveFailures int
	LastRTT             time.Duration // 0 = never measured
}

// State is the group snapshot for the GetDNSGroups RPC.
type State struct {
	Tag      string
	Mode     string
	Winner   string // race only; "" before the first race
	Ranking  []string
	HasRaced bool
	RaceAge  time.Duration // valid when HasRaced
	Members  []MemberState
}

// GroupState returns a point-in-time health snapshot. The daemon handler
// discovers groups by asserting this method on the manager's transports.
func (t *Transport) GroupState() State {
	now := time.Now()
	t.access.Lock()
	defer t.access.Unlock()
	snapshot := State{
		Tag:      t.Tag(),
		Mode:     t.mode,
		Winner:   t.race.winner,
		Ranking:  append([]string(nil), t.race.ranking...),
		HasRaced: !t.race.lastRace.IsZero(),
	}
	if snapshot.HasRaced {
		snapshot.RaceAge = now.Sub(t.race.lastRace)
	}
	for _, current := range t.members {
		memberSnapshot := MemberState{
			Tag:        current.tag,
			ServerType: current.transport.Type(),
			Up:         true,
		}
		if state := t.state[current.tag]; state != nil {
			memberSnapshot.ConsecutiveFailures = state.consecFails
			memberSnapshot.LastRTT = state.lastRTT
			if !state.lastFail.IsZero() {
				if remaining := t.downTime - now.Sub(state.lastFail); remaining > 0 {
					memberSnapshot.Up = false
					memberSnapshot.DownRemaining = remaining
				}
			}
		}
		snapshot.Members = append(snapshot.Members, memberSnapshot)
	}
	return snapshot
}
