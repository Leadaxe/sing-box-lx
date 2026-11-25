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
// One bottleneck per direction, shared by every connection of the outbound:
//
//	start   = max(now, nextFree)           the unit waits for the units ahead
//	backlog = start - now                  the queueing delay it would see
//	backlog > queue limit                  datagram: tail drop; stream: wait (backpressure)
//	nextFree = start + size*8/rate         it occupies the bottleneck for its transmission time
//	due     = nextFree + delay ± jitter    when it comes out of the far end
//
// This is the virtual finish time of a FIFO served at a constant rate: a
// token bucket whose depth is one unit, with a queue in front of it. When
// the rate changes (vary, schedule), units already admitted keep the
// departure they were given; the new rate applies from the next unit.

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

// Stats are the counters of one direction.
type Stats struct {
	Units, Bytes        uint64 // admitted (datagrams delivered later, stream segments)
	DropQueue, DropLoss uint64 // datagrams dropped by the full queue, by random loss
	DropOutage          uint64 // datagrams dropped during an outage
	Stalls              uint64 // stream stalls caused by loss
}

type link struct {
	p           params
	rng         *rand.Rand
	nextFree    int64
	lastDue     int64
	geBad       bool
	varyNext    int64
	schedOutage bool
	resetOutage bool
	stats       Stats
}

// streamState is the per-connection, per-direction state of the stream
// model (guarded by the model's mutex).
type streamState struct {
	lastDue int64
	toLoss  float64 // bytes until the next simulated loss
	lossRef float64 // the loss rate toLoss was drawn for
}

type model struct {
	mu    sync.Mutex
	now   func() int64
	links [2]link
	cfg   *config

	sched        []event // the running schedule: the config's, or one set by ScheduleImpair
	schedStarted bool    // false: a first_use schedule waits for traffic
	schedBase    int64
	nextEvent    int
	seed         int64

	// wake tells the event loop that the next event moved (a first_use
	// schedule started, a command changed the link). It only signals a
	// channel, so it is called with mu held.
	wake func()

	// resetPending: a resetting outage began in an advance whose caller does
	// not act on it (a stream's outage check, the status); the event loop's
	// tick does.
	resetPending bool

	stallRng   *rand.Rand
	stallStart int64
	stallEnd   int64

	nextCheck int64 // before this time advance has nothing to do

	stallOn bool       // the last stall state advance saw (for the log)
	logs    []logEvent // events for the log, taken by tick
}

// logEvent is a link event for the log: schedule entries at INFO, stall
// boundaries at DEBUG.
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
	m := &model{now: now, cfg: cfg, sched: cfg.schedule, seed: seed}
	for dir := range m.links {
		l := &m.links[dir]
		l.p = cfg.dirs[dir]
		l.rng = rand.New(rand.NewPCG(uint64(seed), uint64(dir+1)))
		if l.p.vary != nil && !l.p.drawNow {
			l.varyNext = int64(l.p.vary.every)
		}
	}
	m.stallRng = rand.New(rand.NewPCG(uint64(seed), 3))
	if cfg.stall != nil {
		m.stallStart = m.expSample(cfg.stall.every)
		m.stallEnd = m.stallStart + int64(cfg.stall.duration)
	}
	if !cfg.firstUse {
		m.schedStarted = true
	}
	return m
}

func (m *model) expSample(mean time.Duration) int64 {
	return int64(m.stallRng.ExpFloat64() * float64(mean))
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

// applyEvent applies one change of the link (a schedule entry or a
// SetImpair command) at now. It reports whether a resetting outage began.
func (m *model) applyEvent(ev *event, now int64) (reset bool) {
	for dir, on := range ev.dirs {
		if !on {
			continue
		}
		l := &m.links[dir]
		if ev.link != nil {
			_ = applyLink("", ev.link, &l.p) // validated by resolveEntry
			if l.p.vary != nil && !l.p.drawNow && ev.link.Rate != "" {
				l.varyNext = now + int64(l.p.vary.every)
			}
		}
		if ev.outage != nil {
			l.schedOutage = *ev.outage
			l.resetOutage = *ev.outage && ev.reset
			if l.resetOutage {
				reset = true
			}
		}
	}
	return reset
}

// set applies a change of the link now (SetImpair), on top of whatever the
// schedule did; the schedule keeps running.
func (m *model) set(ev *event) (reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	reset = m.advance(now)
	if m.applyEvent(ev, now) {
		reset = true
	}
	m.note(false, m.describeEvent("set", ev))
	m.nextCheck = 0
	if m.wake != nil {
		m.wake()
	}
	return reset
}

// replaceSchedule starts a new schedule (ScheduleImpair): the link goes
// back to the config's parameters with no outage, the random generators
// restart from the seed (when the config sets one, so a scenario run twice
// draws the same losses for the same traffic), and the entries count from
// now or, with firstUse, from the next traffic. An empty schedule only
// restores the config's link.
func (m *model) replaceSchedule(sched []event, firstUse bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.advance(now)
	for dir := range m.links {
		l := &m.links[dir]
		l.p = m.cfg.dirs[dir]
		l.schedOutage, l.resetOutage, l.geBad = false, false, false
		if m.cfg.seeded {
			l.rng = rand.New(rand.NewPCG(uint64(m.seed), uint64(dir+1)))
		}
		if l.p.vary != nil && !l.p.drawNow {
			l.varyNext = now + int64(l.p.vary.every)
		}
	}
	m.sched, m.nextEvent = sched, 0
	m.schedStarted, m.schedBase = !firstUse, now
	switch {
	case len(sched) == 0:
		m.note(false, "schedule cleared, link back to the config")
	case firstUse:
		m.note(false, "new schedule of "+strconv.Itoa(len(sched))+" entries, link back to the config; starts on first traffic")
	default:
		m.note(false, "new schedule of "+strconv.Itoa(len(sched))+" entries, link back to the config; starts now")
	}
	m.nextCheck = 0
	if m.wake != nil {
		m.wake()
	}
}

// advance applies what became due by now: schedule entries, stall
// boundaries, rate draws. It reports whether a resetting outage began.
// Called with mu held.
func (m *model) advance(now int64) (reset bool) {
	if now < m.nextCheck {
		return false
	}
	next := int64(math.MaxInt64)
	if m.schedStarted {
		for m.nextEvent < len(m.sched) {
			ev := &m.sched[m.nextEvent]
			at := m.schedBase + int64(ev.at)
			if at > now {
				next = min(next, at)
				break
			}
			m.nextEvent++
			if m.applyEvent(ev, now) {
				reset = true
			}
			m.note(false, m.describeEvent("schedule at "+ev.at.String(), ev))
		}
	}
	if stall := m.cfg.stall; stall != nil {
		for now >= m.stallEnd {
			m.stallStart = m.stallEnd + m.expSample(stall.every)
			m.stallEnd = m.stallStart + int64(stall.duration)
		}
		if now < m.stallStart {
			next = min(next, m.stallStart)
		} else {
			next = min(next, m.stallEnd)
		}
		if on := now >= m.stallStart; on != m.stallOn {
			m.stallOn = on
			if on {
				m.note(true, "stall of the whole link for "+stall.duration.String())
			} else {
				m.note(true, "stall ends")
			}
		}
	}
	for dir := range m.links {
		l := &m.links[dir]
		vary := l.p.vary
		if vary == nil {
			continue
		}
		if l.p.drawNow || now >= l.varyNext {
			l.p.rate = vary.min + l.rng.Float64()*(vary.max-vary.min)
			if l.p.drawNow || now-l.varyNext >= int64(vary.every) {
				l.varyNext = now + int64(vary.every)
			} else {
				l.varyNext += int64(vary.every)
			}
			l.p.drawNow = false
		}
		next = min(next, l.varyNext)
	}
	m.nextCheck = next
	if reset {
		m.resetPending = true
		if m.wake != nil {
			m.wake()
		}
	}
	return reset
}

// describeEvent is the log line of a change just applied.
func (m *model) describeEvent(what string, ev *event) string {
	var parts []string
	for dir, on := range ev.dirs {
		if !on {
			continue
		}
		text := dirNames[dir]
		if ev.link != nil {
			text += " " + describeLink(&m.links[dir].p)
		}
		if ev.outage != nil {
			switch {
			case *ev.outage && ev.reset:
				text += " outage begins, connections reset"
			case *ev.outage:
				text += " outage begins"
			default:
				text += " outage ends"
			}
		}
		parts = append(parts, text)
	}
	return what + ": " + strings.Join(parts, "; ")
}

// nextWake is when the next logged event is due: a schedule entry or a
// stall boundary (not a vary draw). ok is false when nothing is pending:
// no stall and the schedule is done or, with first_use, not started.
func (m *model) nextWake() (at int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at = math.MaxInt64
	if m.schedStarted && m.nextEvent < len(m.sched) {
		at = m.schedBase + int64(m.sched[m.nextEvent].at)
	}
	if m.cfg.stall != nil {
		if m.now() < m.stallStart {
			at = min(at, m.stallStart)
		} else {
			at = min(at, m.stallEnd)
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

func (m *model) stalled(now int64) bool {
	return m.cfg.stall != nil && now >= m.stallStart && now < m.stallEnd
}

func (m *model) outageLocked(dir int, now int64) bool {
	return m.links[dir].schedOutage || m.stalled(now)
}

// outage reports whether a direction is down now.
func (m *model) outage(dir int) bool {
	m.mu.Lock()
	now := m.now()
	m.advance(now)
	down := m.outageLocked(dir, now)
	m.mu.Unlock()
	return down
}

// dialState reports whether a new connection can be made: errLinkReset
// during a resetting outage, wait > 0 during a silent one (the handshake
// would go unanswered).
func (m *model) dialState() (resetting bool, wait time.Duration, reset bool) {
	m.mu.Lock()
	now := m.now()
	reset = m.advance(now)
	if m.links[dirUp].resetOutage || m.links[dirDown].resetOutage {
		resetting = true
	} else if m.outageLocked(dirUp, now) || m.outageLocked(dirDown, now) {
		wait = outagePoll
	}
	m.mu.Unlock()
	return
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

// admitPacket runs one datagram of size bytes through a direction: the due
// time of its delivery, or the reason it is dropped.
func (m *model) admitPacket(dir int, size int) (due int64, v verdict, reset bool) {
	m.mu.Lock()
	now := m.now()
	m.useLocked(now)
	reset = m.advance(now)
	l := &m.links[dir]
	if m.outageLocked(dir, now) {
		l.stats.DropOutage++
		m.mu.Unlock()
		return 0, verdictOutage, reset
	}
	depart := now
	if l.p.rate > 0 {
		start := max(now, l.nextFree)
		if start-now > l.queueLimit() {
			l.stats.DropQueue++
			m.mu.Unlock()
			return 0, verdictQueue, reset
		}
		l.nextFree = start + l.txTime(size)
		depart = l.nextFree
	}
	if l.lose() {
		l.stats.DropLoss++
		m.mu.Unlock()
		return 0, verdictLoss, reset
	}
	due = depart + l.delayJitter()
	if !l.p.reorder {
		due = max(due, l.lastDue)
		l.lastDue = due
	}
	l.stats.Units++
	l.stats.Bytes += uint64(size)
	m.mu.Unlock()
	return due, verdictPass, reset
}

// admitStream runs one stream segment through a direction. A stream never
// loses data: a full queue or an outage returns wait > 0 (try again after
// it, which is the backpressure), and loss becomes a stall of the
// connection's delivery (head-of-line: the segments after it wait too).
func (m *model) admitStream(dir int, st *streamState, size int) (due int64, wait time.Duration, reset bool) {
	m.mu.Lock()
	now := m.now()
	m.useLocked(now)
	reset = m.advance(now)
	l := &m.links[dir]
	if m.outageLocked(dir, now) {
		wait = outagePoll
		if !l.schedOutage && m.stallEnd-now < int64(wait) {
			wait = time.Duration(m.stallEnd - now)
		}
		m.mu.Unlock()
		return 0, max(wait, time.Microsecond), reset
	}
	depart := now
	if l.p.rate > 0 {
		start := max(now, l.nextFree)
		if backlog, limit := start-now, l.queueLimit(); backlog > limit {
			m.mu.Unlock()
			return 0, time.Duration(backlog - limit), reset
		}
		l.nextFree = start + l.txTime(size)
		depart = l.nextFree
	}
	due = depart + l.delayJitter()
	if l.p.loss > 0 {
		if st.lossRef != l.p.loss {
			st.lossRef = l.p.loss
			st.toLoss = l.rng.ExpFloat64() * mss / min(l.p.loss, 1)
		}
		st.toLoss -= float64(size)
		if st.toLoss <= 0 {
			// one stall however many segments of this unit were lost:
			// they are recovered together
			for st.toLoss <= 0 {
				st.toLoss += l.rng.ExpFloat64() * mss / min(l.p.loss, 1)
			}
			due += int64(l.streamStall())
			l.stats.Stalls++
		}
	} else {
		st.lossRef = 0
	}
	due = max(due, st.lastDue)
	st.lastDue = due
	l.stats.Units++
	l.stats.Bytes += uint64(size)
	m.mu.Unlock()
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

func (m *model) stats(dir int) Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links[dir].stats
}

// rate is the current rate of a direction (bit/s, 0 = unlimited).
func (m *model) rate(dir int) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.advance(m.now())
	return m.links[dir].p.rate
}
