package impair

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
)

// newResetError is what a connection broken by a resetting outage returns,
// and what a dial during one fails with: ECONNRESET, with what began it (a
// schedule entry, the impair set command, a sublink).
func newResetError(cause string) error {
	if cause == "" {
		cause = "an outage"
	}
	return E.Extend(syscall.ECONNRESET, "impair: link reset by "+cause)
}

// shaper is the bottleneck of one outbound instance: the model, the two
// datagram pumps and the set of live connections (for resets).
type shaper struct {
	model *model
	pumps [2]*pump
	now   func() int64

	mu    sync.Mutex
	conns map[resetter]struct{}
	done  bool

	// connectTimeout bounds a dial held by a silent outage: the outbound's
	// connect_timeout, else the dialer's default.
	connectTimeout time.Duration

	// logf writes a link event to the outbound's log (nil: no log).
	logf      func(debug bool, text string)
	eventOnce sync.Once
	kick      chan struct{} // to the event loop: a first_use schedule started
	stopped   chan struct{}
}

type resetter interface {
	reset(err error)
}

func newShaper(cfg *config, now func() int64) *shaper {
	s := &shaper{now: now, connectTimeout: C.TCPConnectTimeout, conns: make(map[resetter]struct{}), kick: make(chan struct{}, 1), stopped: make(chan struct{})}
	s.model = newModel(cfg, now)
	s.model.wake = func() { signal(s.kick) }
	s.pumps[dirUp] = newPump(dirUp, now)
	s.pumps[dirDown] = newPump(dirDown, now)
	return s
}

func realClock() func() int64 {
	base := time.Now()
	return func() int64 { return int64(time.Since(base)) }
}

func (s *shaper) register(c resetter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *shaper) unregister(c resetter) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// resetAll breaks every live connection (a resetting outage began on the
// outbound's line).
func (s *shaper) resetAll() {
	err := newResetError(s.model.resetCause())
	s.mu.Lock()
	conns := make([]resetter, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.reset(err)
	}
}

func (s *shaper) handleReset(reset bool) {
	if reset {
		go s.resetAll()
	}
}

// beforeDial starts a first_use schedule and holds a dial while the link is
// down: a silent outage makes it wait (an unanswered handshake) until the
// link is back or ctx ends; a resetting one fails it at once.
//
// The wait is bounded by the connect timeout (connectTimeout), as a real
// handshake that gets no answer: the dial fails with a timeout instead of
// hanging to the end of the outage and then going out in a burst.
func (s *shaper) beforeDial(ctx context.Context, sub *subline) error {
	s.startEvents()
	s.model.firstUse()
	var timer *time.Timer
	var limit <-chan time.Time
	for {
		refused, wait, reset := s.model.dialState(sub)
		s.handleReset(reset)
		if refused != nil {
			return refused
		}
		if wait == 0 {
			return nil
		}
		if limit == nil {
			limitTimer := time.NewTimer(s.connectTimeout)
			defer limitTimer.Stop()
			limit = limitTimer.C
		}
		if timer == nil {
			timer = time.NewTimer(wait)
			defer timer.Stop()
		} else {
			timer.Reset(wait)
		}
		select {
		case <-timer.C:
		case <-ctx.Done():
			return E.Cause(ctx.Err(), "impair: link down")
		case <-limit:
			return E.Cause(os.ErrDeadlineExceeded, "impair: link down: no answer within the connect timeout ", s.connectTimeout)
		}
	}
}

// eventWakeMin keeps the event loop from spinning on an event that is due
// but not yet applied.
const eventWakeMin = 100 * time.Microsecond

// startEvents starts the event loop once (on the outbound's start, or the
// first dial if that comes first).
func (s *shaper) startEvents() {
	s.eventOnce.Do(func() { go s.eventLoop() })
}

// eventLoop applies the schedule entries and stall boundaries on time even
// when no traffic passes (the model otherwise advances lazily, on the next
// unit of data): resets happen at their moment and the log shows each
// event when it happens.
func (s *shaper) eventLoop() {
	timer := newStoppedTimer()
	for {
		reset, logs := s.model.tick()
		if s.logf != nil {
			for _, l := range logs {
				s.logf(l.debug, l.text)
			}
		}
		s.handleReset(reset)
		at, ok := s.model.nextWake()
		if !ok {
			select {
			case <-s.kick:
				continue
			case <-s.stopped:
				return
			}
		}
		wait := max(time.Duration(at-s.now()), eventWakeMin)
		if !sleep(timer, wait, s.stopped, s.kick, nil) && isClosed(s.stopped) {
			return
		}
	}
}

func (s *shaper) close() {
	s.mu.Lock()
	if !s.done {
		close(s.stopped)
	}
	s.done = true
	conns := make([]resetter, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if closer, ok := c.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	s.pumps[dirUp].close()
	s.pumps[dirDown].close()
}

// wrapConn wraps what the carrier dialler returned for network; sub is the
// connection's sublink (nil: the outbound's line only).
func (s *shaper) wrapConn(network string, conn net.Conn, sub *subline) net.Conn {
	if network == "udp" {
		return newDatagramConn(s, conn, sub)
	}
	return newStreamConn(s, conn, sub)
}

// connInfo is what a sublink matches a new connection on.
type connInfo struct {
	network string
	domain  string
	addr    netip.Addr
	port    uint16
}

// sleep waits d, or until one of the channels is closed/signalled (nil
// channels never fire). It reports whether the full time passed.
func sleep(timer *time.Timer, d time.Duration, a, b, c <-chan struct{}) bool {
	timer.Reset(d)
	select {
	case <-timer.C:
		return true
	case <-a:
	case <-b:
	case <-c:
	}
	timer.Stop()
	return false
}

func newStoppedTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}

// deadline is net.Pipe's deadline: a channel closed when the time passes.
type deadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{}
}

func makeDeadline() deadline {
	return deadline{cancel: make(chan struct{})}
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // the timer fired and closed it: wait for that
	}
	d.timer = nil
	closed := isClosed(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		cancel := d.cancel
		d.timer = time.AfterFunc(dur, func() { close(cancel) })
		return
	}
	if !closed {
		close(d.cancel)
	}
}

func (d *deadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
