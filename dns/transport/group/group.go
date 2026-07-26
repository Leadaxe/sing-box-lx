// Package group implements the lx `group` DNS server type (SPEC 033/034):
// several member servers behind one tag with a selection strategy. Failover
// walks the member list in order, skipping servers inside their down_time
// window; race (race.go) periodically fans a real query out to all members
// and pins the fastest responder until the next race.
//
// The group sits UNDER the DNS cache (cache keys carry the group tag), so
// whatever the group returns is cached once for the whole group; member
// responses that the group discards never reach the cache.
package group

import (
	"context"
	"errors"
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

type Transport struct {
	dns.TransportAdapter
	ctx        context.Context
	logger     log.ContextLogger
	serverTags []string
	mode       string
	interval   time.Duration
	downTime   time.Duration

	access   sync.Mutex
	members  []*member
	lastFail map[string]time.Time

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
		lastFail:         make(map[string]time.Time),
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

// Reset drops health state: down marks and (in race mode) the winner and
// ranking. A network change invalidates both. Members are reset by the
// manager's own loop; the group must not fan Reset out.
func (t *Transport) Reset() {
	t.access.Lock()
	defer t.access.Unlock()
	t.lastFail = make(map[string]time.Time)
	t.race.reset()
}

func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
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
	response, err := current.transport.Exchange(ctx, message)
	if isFailure(response, err) {
		t.markFailure(current.tag)
		t.logFailure(ctx, current.tag, response, err)
		return response, err
	}
	t.clearFailure(current.tag)
	t.recordEffective(ctx, current)
	return response, err
}

// recordEffective attributes the answer to the member that produced it for
// the DNS query stream (SPEC 035). No-op without the client-level holder.
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
		if failedAt, down := t.lastFail[current.tag]; !down || now.Sub(failedAt) >= t.downTime {
			alive = append(alive, current)
		}
	}
	return alive
}

func (t *Transport) oldestFailedMember() *member {
	t.access.Lock()
	defer t.access.Unlock()
	oldest := t.members[0]
	oldestAt := t.lastFail[oldest.tag]
	for _, current := range t.members[1:] {
		if failedAt := t.lastFail[current.tag]; failedAt.Before(oldestAt) {
			oldest, oldestAt = current, failedAt
		}
	}
	return oldest
}

func (t *Transport) markFailure(tag string) {
	t.access.Lock()
	defer t.access.Unlock()
	t.lastFail[tag] = time.Now()
}

func (t *Transport) clearFailure(tag string) {
	t.access.Lock()
	defer t.access.Unlock()
	delete(t.lastFail, tag)
}
