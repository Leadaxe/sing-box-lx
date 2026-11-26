package impair

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The link model: everything that decides what happens to a unit of data
// (a datagram, or a segment of a stream) is here, as a pure function of the
// clock, so the tests drive it with a fake clock. Times are int64
// nanoseconds since the model's base (the outbound's creation).
//
// A line has one bottleneck per direction, shared by every connection that
// goes through it:
//
//	start   = max(arrive, nextFree)        the unit waits for the units ahead
//	backlog = start - arrive               the queueing delay it would see
//	backlog > queue limit                  datagram: tail drop; stream: wait (backpressure)
//	nextFree = start + size*8/rate         it occupies the bottleneck for its transmission time
//	due     = nextFree + delay ± jitter    when it comes out of the far end
//
// This is the virtual finish time of a FIFO served at a constant rate: a
// token bucket whose depth is one unit, with a queue in front of it. When
// the rate changes (vary, schedule), units already admitted keep the
// departure they were given; the new rate applies from the next unit.
//
// Every connection goes through the outbound's own line. A connection that
// matched a sublink when it opened then goes through the sublink's line
// too, as a second stage: it arrives there when it comes out of the first.

// mss is the stream unit for the loss-as-stall model (a TCP segment).
const mss = 1448

// outagePoll is how often a stream stalled by an outage rechecks the link.
const outagePoll = 5 * time.Millisecond

// streamStallMin is the floor of a stream stall: Linux's minimum RTO.
const streamStallMin = 200 * time.Millisecond

type verdict uint8

const (
	verdictPass verdict = iota
	verdictQueue
	verdictLoss
	verdictOutage
)

// Stats are the counters of one direction of a line.
type Stats struct {
	Units, Bytes        uint64 // admitted (datagrams delivered later, stream segments)
	DropQueue, DropLoss uint64 // datagrams dropped by the full queue, by random loss
	DropOutage          uint64 // datagrams dropped during an outage
	Stalls              uint64 // stream stalls caused by loss
}

// link is one direction of a line.
type link struct {
	p           params
	rng         *rand.Rand
	nextFree    int64
	lastDue     int64
	geBad       bool
	varyNext    int64
	resetOutage bool
	stats       Stats
}

// streamState is the per-connection, per-direction, per-stage state of the
// stream model (guarded by the model's mutex).
type streamState struct {
	lastDue int64
	toLoss  float64 // bytes until the next simulated loss
	lossRef float64 // the loss rate toLoss was drawn for
}

// streamStates is a stream connection's state: [direction][stage].
type streamStates [2][2]streamState

// line is the outbound's own line or a sublink's.
type line struct {
	links      [2]link
	stall      *stallSpec
	stallRng   *rand.Rand
	stallStart int64
	stallEnd   int64
	stallOn    bool // the last stall state advance saw (for the log)
	profile    string
	resetBy    string // what began the resetting outage: "schedule at 1m0s", "impair set", "sublink [...]"
}

// newLine starts a line on v. stream separates the random generators of
// the lines of one model: the outbound's line is 0.
func newLine(v lineValues, seed int64, stream uint64, now int64) *line {
	l := &line{}
	for dir := range l.links {
		l.links[dir].rng = rand.New(rand.NewPCG(uint64(seed), stream*4+uint64(dir+1)))
	}
	l.stallRng = rand.New(rand.NewPCG(uint64(seed), stream*4+3))
	for dir := range l.links {
		k := &l.links[dir]
		k.p = v.dirs[dir]
		if k.p.vary != nil && !k.p.drawNow {
			k.varyNext = now + int64(k.p.vary.every)
		}
	}
	l.profile = v.profile
	l.setStall(v.stall, now)
	return l
}

func (l *line) values() lineValues {
	return lineValues{dirs: [2]params{l.links[dirUp].p, l.links[dirDown].p}, stall: l.stall, profile: l.profile}
}

func (l *line) expSample(mean time.Duration) int64 {
	return int64(l.stallRng.ExpFloat64() * float64(mean))
}

func (l *line) setStall(spec *stallSpec, now int64) {
	l.stall = spec
	if spec != nil {
		l.stallStart = now + l.expSample(spec.every)
		l.stallEnd = l.stallStart + int64(spec.duration)
	}
}

// apply applies checked link_params at now; cause names the change for the
// error a reset gives. It reports whether a resetting outage began.
func (l *line) apply(profiles *profileSet, lp *LinkParams, now int64, cause string) (reset bool) {
	v := l.values()
	outageSet, _ := profiles.apply("", lp, &v, 0) // checked by resolveEntry
	switched := lp.Profile != ""
	for dir := range l.links {
		k := &l.links[dir]
		k.p = v.dirs[dir]
		if switched {
			k.geBad = false
		}
		own := [2]*LinkOptions{lp.Up, lp.Down}[dir]
		touched := switched || lp.Rate != "" || lp.Vary != nil || own != nil && (own.Rate != "" || own.Vary != nil)
		if touched && k.p.vary != nil && !k.p.drawNow {
			k.varyNext = now + int64(k.p.vary.every)
		}
		if outageSet[dir] {
			k.resetOutage = k.p.outage && lp.Reset
			if k.resetOutage {
				reset = true
			}
		}
		if !k.p.outage {
			k.resetOutage = false
		}
	}
	if reset {
		l.resetBy = cause
	}
	l.profile = v.profile
	if switched || lp.Stall != nil {
		l.setStall(v.stall, now)
	}
	return reset
}

// advance applies the stall boundaries and rate draws due by now and
// reports when the next one is.
func (l *line) advance(now int64, note func(debug bool, text string), name string) (next int64) {
	next = math.MaxInt64
	if stall := l.stall; stall != nil {
		for now >= l.stallEnd {
			l.stallStart = l.stallEnd + l.expSample(stall.every)
			l.stallEnd = l.stallStart + int64(stall.duration)
		}
		if now < l.stallStart {
			next = min(next, l.stallStart)
		} else {
			next = min(next, l.stallEnd)
		}
	}
	if on := l.stall != nil && now >= l.stallStart; on != l.stallOn {
		l.stallOn = on
		if on {
			note(true, name+"stall of the whole line for "+l.stall.duration.String())
		} else {
			note(true, name+"stall ends")
		}
	}
	for dir := range l.links {
		k := &l.links[dir]
		vary := k.p.vary
		if vary == nil {
			continue
		}
		if k.p.drawNow || now >= k.varyNext {
			k.p.rate = vary.min + k.rng.Float64()*(vary.max-vary.min)
			if k.p.drawNow || now-k.varyNext >= int64(vary.every) {
				k.varyNext = now + int64(vary.every)
			} else {
				k.varyNext += int64(vary.every)
			}
			k.p.drawNow = false
		}
		next = min(next, k.varyNext)
	}
	return next
}

func (l *line) stalled(now int64) bool {
	return l.stall != nil && now >= l.stallStart && now < l.stallEnd
}

func (l *line) down(dir int, now int64) bool {
	return l.links[dir].p.outage || l.stalled(now)
}

func (l *line) resetting() bool {
	return l.links[dirUp].resetOutage || l.links[dirDown].resetOutage
}

// resetError is what a connection broken, or a dial refused, by this
// line's resetting outage gets: ECONNRESET with the cause.
func (l *line) resetError() error {
	return newResetError(l.resetBy)
}

// describe is the line for the log: template, both directions, stalls.
func (l *line) describe() string {
	var text string
	if l.profile != "" {
		text = "profile " + l.profile + "; "
	}
	for dir := range l.links {
		if dir > 0 {
			text += "; "
		}
		p := &l.links[dir].p
		text += dirNames[dir] + " " + describeLink(p)
		if p.outage {
			text += " outage"
			if l.links[dir].resetOutage {
				text += " (reset)"
			}
		}
	}
	if l.stall != nil {
		text += "; stall every " + l.stall.every.String() + " for " + l.stall.duration.String()
	}
	return text
}

// subline is a sublink at runtime: its match, its line and the state of
// every / share.
type subline struct {
	spec     sublinkSpec
	line     *line
	seen     uint64 // connections that matched the fields
	matched  uint64 // connections that took the sublink
	shareRng *rand.Rand
}

func (s *subline) name() string {
	return "sublink " + s.spec.describe() + ": "
}

func (s *sublinkSpec) describe() string {
	var parts []string
	o := s.option.Match
	if len(o.Domain) > 0 {
		parts = append(parts, "domain "+strings.Join(o.Domain, ","))
	}
	if len(o.DomainSuffix) > 0 {
		parts = append(parts, "domain_suffix "+strings.Join(o.DomainSuffix, ","))
	}
	if len(o.IPCIDR) > 0 {
		parts = append(parts, "ip_cidr "+strings.Join(o.IPCIDR, ","))
	}
	if len(o.Port) > 0 {
		ports := make([]string, len(o.Port))
		for i, port := range o.Port {
			ports[i] = strconv.Itoa(int(port))
		}
		parts = append(parts, "port "+strings.Join(ports, ","))
	}
	if len(o.Network) > 0 {
		parts = append(parts, "network "+strings.Join(o.Network, ","))
	}
	if o.Every > 0 {
		parts = append(parts, "every "+strconv.Itoa(o.Every))
	}
	if o.Share != "" {
		parts = append(parts, "share "+o.Share)
	}
	if len(parts) == 0 {
		return "[every connection]"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

type model struct {
	mu       sync.Mutex
	now      func() int64
	cfg      *config
	line     *line
	sublinks []*subline

	sched        []event // the running schedule: the config's, or one set by ScheduleImpair
	repeat       time.Duration
	round        int
	schedStarted bool // false: a first_use schedule waits for traffic
	schedBase    int64
	nextEvent    int
	seed         int64
	generation   uint64 // the random stream of the next sublink

	// wake tells the event loop that the next event moved (a first_use
	// schedule started, a command changed the link). It only signals a
	// channel, so it is called with mu held.
	wake func()

	// resetPending: a resetting outage began in an advance whose caller does
	// not act on it (a stream's outage check, the status); the event loop's
	// tick does.
	resetPending bool

	nextCheck int64 // before this time advance has nothing to do

	logs []logEvent // events for the log, taken by tick
}

// logEvent is a link event for the log: schedule entries and commands at
// INFO, stall boundaries at DEBUG.
type logEvent struct {
	debug bool
	text  string
}

// maxPendingLogs bounds the events waiting for tick (a model without the
// event loop, as in the unit tests, drops the rest).
const maxPendingLogs = 256

func (m *model) note(debug bool, text string) {
	if len(m.logs) < maxPendingLogs {
		m.logs = append(m.logs, logEvent{debug: debug, text: text})
	}
}

func newModel(cfg *config, now func() int64) *model {
	seed := cfg.seed
	if !cfg.seeded {
		seed = rand.Int64()
	}
	m := &model{now: now, cfg: cfg, seed: seed, sched: cfg.schedule, repeat: cfg.repeat}
	m.line = newLine(cfg.line, seed, 0, 0)
	m.sublinks = m.buildSublinks(cfg.sublinks, 0)
	m.schedStarted = !cfg.firstUse
	return m
}

func (m *model) buildSublinks(specs []sublinkSpec, now int64) []*subline {
	sublinks := make([]*subline, 0, len(specs))
	for _, spec := range specs {
		m.generation++
		sub := &subline{spec: spec, line: newLine(spec.line, m.seed, m.generation, now)}
		sub.shareRng = rand.New(rand.NewPCG(uint64(m.seed), m.generation*4))
		if spec.reset {
			for dir := range sub.line.links {
				sub.line.links[dir].resetOutage = sub.line.links[dir].p.outage
			}
			sub.line.resetBy = "sublink " + spec.describe()
		}
		sublinks = append(sublinks, sub)
	}
	return sublinks
}

// firstUse starts a "start": "first_use" schedule on a new connection.
func (m *model) firstUse() {
	m.mu.Lock()
	m.useLocked(m.now())
	m.mu.Unlock()
}

// useLocked starts a schedule that waits for traffic: a new connection or
// a unit of data in either direction, whichever comes first (connections
// opened before the schedule was set may carry data for minutes without a
// new dial).
func (m *model) useLocked(now int64) {
	if m.schedStarted {
		return
	}
	m.schedStarted = true
	m.schedBase = now
	m.nextCheck = 0
	if len(m.sched) > 0 {
		m.note(false, "schedule starts: first traffic")
	}
	if m.wake != nil {
		m.wake()
	}
}

// pick finds the sublink of a new connection: the first whose fields match
// and whose every / share select it. nil: the outbound's line only.
func (m *model) pick(conn connInfo) *subline {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.sublinks {
		match := &sub.spec.match
		if !match.fields(conn.network, conn.domain, conn.addr, conn.port) {
			continue
		}
		sub.seen++
		if match.every > 0 && sub.seen%uint64(match.every) != 0 {
			continue
		}
		if match.share > 0 && sub.shareRng.Float64() >= match.share {
			continue
		}
		sub.matched++
		return sub
	}
	return nil
}

// applyEvent applies a schedule entry or a SetImpair command at now. It
// reports whether a resetting outage began on the outbound's line.
func (m *model) applyEvent(ev *event, now int64, cause string) (reset bool) {
	if ev.link != nil {
		reset = m.line.apply(m.cfg.profiles, ev.link.lp, now, cause)
	}
	if ev.sublinks != nil {
		m.sublinks = m.buildSublinks(*ev.sublinks, now)
	}
	return reset
}

// set applies a change now (SetImpair), on top of whatever the schedule
// did; the schedule keeps running.
func (m *model) set(ev *event) (reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	reset = m.advance(now)
	if m.applyEvent(ev, now, "impair set") {
		reset = true
	}
	m.note(false, m.describeEvent("set", ev))
	m.nextCheck = 0
	if m.wake != nil {
		m.wake()
	}
	return reset
}

// replaceSchedule starts a new schedule (ScheduleImpair): the line and the
// sublinks go back to the config, the random generators restart from the
// seed (so a scenario run twice draws the same for the same traffic), and
// the entries count from now or, with firstUse, from the next traffic. An
// empty schedule only restores the config. The counters are kept.
func (m *model) replaceSchedule(sched []event, firstUse bool, repeat time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.advance(now)
	stats := [2]Stats{m.line.links[dirUp].stats, m.line.links[dirDown].stats}
	m.line = newLine(m.cfg.line, m.seed, 0, now)
	m.line.links[dirUp].stats, m.line.links[dirDown].stats = stats[dirUp], stats[dirDown]
	m.generation = 0
	m.sublinks = m.buildSublinks(m.cfg.sublinks, now)
	m.sched, m.repeat, m.round, m.nextEvent = sched, repeat, 0, 0
	m.schedStarted, m.schedBase = !firstUse, now
	text := "link and sublinks back to the config"
	switch {
	case len(sched) == 0:
		text = "schedule cleared, " + text
	case firstUse:
		text = "new schedule of " + strconv.Itoa(len(sched)) + " entries, " + text + "; starts on first traffic"
	default:
		text = "new schedule of " + strconv.Itoa(len(sched)) + " entries, " + text + "; starts now"
	}
	if repeat > 0 && len(sched) > 0 {
		text += ", repeats every " + repeat.String()
	}
	m.note(false, text)
	m.nextCheck = 0
	if m.wake != nil {
		m.wake()
	}
}

// advance applies what became due by now on the outbound's line: schedule
// entries, stall boundaries, rate draws. It reports whether a resetting
// outage began. Called with mu held.
func (m *model) advance(now int64) (reset bool) {
	if now < m.nextCheck {
		return false
	}
	next := int64(math.MaxInt64)
	if m.schedStarted && len(m.sched) > 0 {
		for {
			if m.nextEvent < len(m.sched) {
				ev := &m.sched[m.nextEvent]
				if at := m.schedBase + int64(ev.at); at > now {
					next = min(next, at)
					break
				}
				m.nextEvent++
				if m.applyEvent(ev, now, "schedule at "+ev.at.String()) {
					reset = true
				}
				m.note(false, m.describeEvent("schedule at "+ev.at.String(), ev))
				continue
			}
			if m.repeat <= 0 {
				break
			}
			roundEnd := m.schedBase + int64(m.repeat)
			if roundEnd > now {
				next = min(next, roundEnd+int64(m.sched[0].at))
				break
			}
			m.schedBase, m.nextEvent = roundEnd, 0
			m.round++
		}
	}
	next = min(next, m.line.advance(now, m.note, ""))
	m.nextCheck = next
	if reset {
		m.resetPending = true
		if m.wake != nil {
			m.wake()
		}
	}
	return reset
}

// advanceSub brings a connection's sublink up to now.
func (m *model) advanceSub(sub *subline, now int64) {
	if sub != nil {
		sub.line.advance(now, m.note, sub.name())
	}
}

// describeEvent is the log line of a change just applied.
func (m *model) describeEvent(what string, ev *event) string {
	var parts []string
	if ev.link != nil {
		parts = append(parts, m.line.describe())
	}
	if ev.sublinks != nil {
		if len(m.sublinks) == 0 {
			parts = append(parts, "no sublinks")
		}
		for _, sub := range m.sublinks {
			parts = append(parts, "sublink "+sub.spec.describe()+" "+sub.line.describe())
		}
	}
	return what + ": " + strings.Join(parts, "; ")
}

// nextWake is when the next logged event is due: a schedule entry or a
// stall boundary of the outbound's line (not a vary draw). ok is false when
// nothing is pending.
func (m *model) nextWake() (at int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at = math.MaxInt64
	if m.schedStarted && len(m.sched) > 0 {
		if m.nextEvent < len(m.sched) {
			at = m.schedBase + int64(m.sched[m.nextEvent].at)
		} else if m.repeat > 0 {
			at = m.schedBase + int64(m.repeat) + int64(m.sched[0].at)
		}
	}
	if l := m.line; l.stall != nil {
		if m.now() < l.stallStart {
			at = min(at, l.stallStart)
		} else {
			at = min(at, l.stallEnd)
		}
	}
	return at, at != math.MaxInt64
}

// tick applies what became due by now and takes the events for the log.
func (m *model) tick() (reset bool, logs []logEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reset = m.advance(m.now()) || m.resetPending
	m.resetPending = false
	logs, m.logs = m.logs, nil
	return reset, logs
}

// outage reports whether a direction is down now for a connection.
func (m *model) outage(sub *subline, dir int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.advance(now)
	m.advanceSub(sub, now)
	return m.line.down(dir, now) || sub != nil && sub.line.down(dir, now)
}

// dialState reports whether a new connection can be made: refused (the
// error to fail it with) during a resetting outage, wait > 0 while the
// sending side is down or the line is in a random stall (the handshake gets
// no answer). A receive-only outage lets the connection open: it just gets
// nothing back.
func (m *model) dialState(sub *subline) (refused error, wait time.Duration, reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	reset = m.advance(now)
	m.advanceSub(sub, now)
	switch {
	case m.line.resetting():
		refused = m.line.resetError()
	case sub != nil && sub.line.resetting():
		refused = sub.line.resetError()
	case m.line.down(dirUp, now) || sub != nil && sub.line.down(dirUp, now):
		wait = outagePoll
	}
	return refused, wait, reset
}

// resetCause is the cause of the outbound line's resetting outage.
func (m *model) resetCause() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.line.resetBy
}

func (l *link) queueLimit() int64 {
	if l.p.queueBytes > 0 {
		return int64(float64(l.p.queueBytes) * 8 / l.p.rate * 1e9)
	}
	return int64(l.p.queueTime)
}

func (l *link) txTime(size int) int64 {
	return int64(float64(size) * 8 / l.p.rate * 1e9)
}

func (l *link) delayJitter() int64 {
	d := int64(l.p.delay)
	if l.p.jitter > 0 {
		d += int64((l.rng.Float64()*2 - 1) * float64(l.p.jitter))
		if d < 0 {
			d = 0
		}
	}
	return d
}

// lose draws the random loss of one datagram: independent with probability
// loss, or the two-state Gilbert model when burst > 1 (a lossless good
// state and an all-loss bad state; mean bad run = burst, stationary loss =
// loss).
func (l *link) lose() bool {
	loss := l.p.loss
	if loss <= 0 {
		return false
	}
	if loss >= 1 {
		return true
	}
	if l.p.burst <= 1 {
		return l.rng.Float64() < loss
	}
	if l.geBad {
		if l.rng.Float64() < 1/l.p.burst {
			l.geBad = false
		}
	} else if l.rng.Float64() < loss/(1-loss)/l.p.burst {
		l.geBad = true
	}
	return l.geBad
}

// admitPacketStage runs a datagram through one direction of a line, where
// it arrives at arrive: the due time it comes out, or why it is dropped.
func (l *line) admitPacketStage(dir int, now, arrive int64, size int) (int64, verdict) {
	k := &l.links[dir]
	if l.down(dir, now) {
		k.stats.DropOutage++
		return 0, verdictOutage
	}
	depart := arrive
	if k.p.rate > 0 {
		start := max(arrive, k.nextFree)
		if start-arrive > k.queueLimit() {
			k.stats.DropQueue++
			return 0, verdictQueue
		}
		k.nextFree = start + k.txTime(size)
		depart = k.nextFree
	}
	if k.lose() {
		k.stats.DropLoss++
		return 0, verdictLoss
	}
	due := depart + k.delayJitter()
	if !k.p.reorder {
		due = max(due, k.lastDue)
		k.lastDue = due
	}
	k.stats.Units++
	k.stats.Bytes += uint64(size)
	return due, verdictPass
}

// admitPacket runs one datagram of size bytes through a direction: the
// outbound's line, then the connection's sublink. It returns the due time
// of its delivery, or the reason it is dropped.
func (m *model) admitPacket(sub *subline, dir int, size int) (due int64, v verdict, reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.useLocked(now)
	reset = m.advance(now)
	due, v = m.line.admitPacketStage(dir, now, now, size)
	if v != verdictPass || sub == nil {
		return due, v, reset
	}
	m.advanceSub(sub, now)
	due, v = sub.line.admitPacketStage(dir, now, due, size)
	return due, v, reset
}

// outageWait is how long a stream waits before rechecking a line that is
// down: the end of a random stall, or the outage poll.
func (l *line) outageWait(dir int, now int64) time.Duration {
	wait := outagePoll
	if !l.links[dir].p.outage && l.stallEnd-now < int64(wait) {
		wait = time.Duration(l.stallEnd - now)
	}
	return max(wait, time.Microsecond)
}

// backlogWait is how long a unit arriving at arrive waits before the queue
// of one direction has room for it (0: it has room).
func (k *link) backlogWait(arrive int64) time.Duration {
	if k.p.rate <= 0 {
		return 0
	}
	start := max(arrive, k.nextFree)
	if backlog, limit := start-arrive, k.queueLimit(); backlog > limit {
		return time.Duration(backlog - limit)
	}
	return 0
}

// commitStream admits a stream segment arriving at arrive into one
// direction of a line and returns its due time.
func (k *link) commitStream(arrive int64, st *streamState, size int) int64 {
	depart := arrive
	if k.p.rate > 0 {
		start := max(arrive, k.nextFree)
		k.nextFree = start + k.txTime(size)
		depart = k.nextFree
	}
	due := depart + k.delayJitter()
	if k.p.loss > 0 {
		if st.lossRef != k.p.loss {
			st.lossRef = k.p.loss
			st.toLoss = k.rng.ExpFloat64() * mss / min(k.p.loss, 1)
		}
		st.toLoss -= float64(size)
		if st.toLoss <= 0 {
			// one stall however many segments of this unit were lost:
			// they are recovered together
			for st.toLoss <= 0 {
				st.toLoss += k.rng.ExpFloat64() * mss / min(k.p.loss, 1)
			}
			due += int64(k.streamStall())
			k.stats.Stalls++
		}
	} else {
		st.lossRef = 0
	}
	due = max(due, st.lastDue)
	st.lastDue = due
	k.stats.Units++
	k.stats.Bytes += uint64(size)
	return due
}

// admitStream runs one stream segment through a direction: the outbound's
// line, then the connection's sublink. A stream never loses data: a full
// queue or an outage on either returns wait > 0 (try again after it, which
// is the backpressure), and loss becomes a stall of the connection's
// delivery (head-of-line: the segments after it wait too).
func (m *model) admitStream(sub *subline, dir int, st *streamStates, size int) (due int64, wait time.Duration, reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.useLocked(now)
	reset = m.advance(now)
	m.advanceSub(sub, now)
	if m.line.down(dir, now) {
		return 0, m.line.outageWait(dir, now), reset
	}
	if sub != nil && sub.line.down(dir, now) {
		return 0, sub.line.outageWait(dir, now), reset
	}
	first := &m.line.links[dir]
	if wait = first.backlogWait(now); wait > 0 {
		return 0, wait, reset
	}
	if sub != nil {
		// where the segment would reach the sublink, without the jitter draw
		arrive := now + int64(first.p.delay)
		if first.p.rate > 0 {
			arrive += max(now, first.nextFree) - now + first.txTime(size)
		}
		if wait = sub.line.links[dir].backlogWait(arrive); wait > 0 {
			return 0, wait, reset
		}
	}
	due = first.commitStream(now, &st[dir][0], size)
	if sub != nil {
		due = sub.line.links[dir].commitStream(due, &st[dir][1], size)
	}
	return due, 0, reset
}

// streamStall is the head-of-line delay a loss costs a stream: the
// configured stream_stall, else max(200ms, 2 × delay + 4 × jitter) (an
// RTO: the minimum Linux RTO, or the RTT plus its variation).
func (l *link) streamStall() time.Duration {
	if l.p.streamStall > 0 {
		return l.p.streamStall
	}
	return max(streamStallMin, 2*l.p.delay+4*l.p.jitter)
}

// stats are the counters of a direction of the outbound's line.
func (m *model) stats(dir int) Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.line.links[dir].stats
}

// rate is the current rate of a direction of the outbound's line (bit/s,
// 0 = unlimited).
func (m *model) rate(dir int) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.advance(m.now())
	return m.line.links[dir].p.rate
}
