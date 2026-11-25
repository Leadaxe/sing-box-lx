package impair

import (
	"math"
	"math/rand/v2"
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

	schedStarted bool
	schedBase    int64
	nextEvent    int

	stallRng   *rand.Rand
	stallStart int64
	stallEnd   int64

	nextCheck int64 // before this time advance has nothing to do
}

func newModel(cfg *config, now func() int64) *model {
	seed := cfg.seed
	if !cfg.seeded {
		seed = rand.Int64()
	}
	m := &model{now: now, cfg: cfg}
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

// firstUse starts a "start": "first_use" schedule.
func (m *model) firstUse() {
	m.mu.Lock()
	if !m.schedStarted {
		m.schedStarted = true
		m.schedBase = m.now()
		m.nextCheck = 0
	}
	m.mu.Unlock()
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
		for m.nextEvent < len(m.cfg.schedule) {
			ev := &m.cfg.schedule[m.nextEvent]
			at := m.schedBase + int64(ev.at)
			if at > now {
				next = min(next, at)
				break
			}
			m.nextEvent++
			for dir, on := range ev.dirs {
				if !on {
					continue
				}
				l := &m.links[dir]
				if ev.link != nil {
					_ = applyLink("", ev.link, &l.p) // validated by resolve
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
	return reset
}

// resetTimes are the moments of the resetting outages still to come (the
// schedule must act on them even without traffic). Empty before a
// first_use schedule has started.
func (m *model) resetTimes() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.schedStarted {
		return nil
	}
	var times []int64
	for _, ev := range m.cfg.schedule[m.nextEvent:] {
		if ev.reset {
			times = append(times, m.schedBase+int64(ev.at))
		}
	}
	return times
}

// tick applies what became due by now.
func (m *model) tick() (reset bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advance(m.now())
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
