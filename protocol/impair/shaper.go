package impair

import (
	"context"
	"net"
	"sync"
	"syscall"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// errLinkReset is what a connection broken by a resetting outage returns,
// and what a dial during one fails with.
var errLinkReset = E.Extend(syscall.ECONNRESET, "impair: link reset by schedule")

// shaper is the bottleneck of one outbound instance: the model, the two
// datagram pumps and the set of live connections (for resets).
type shaper struct {
	model *model
	pumps [2]*pump
	now   func() int64

	mu    sync.Mutex
	conns map[resetter]struct{}
	done  bool

	resetOnce sync.Once
	stopped   chan struct{}
}

type resetter interface {
	reset()
}

func newShaper(cfg *config, now func() int64) *shaper {
	s := &shaper{now: now, conns: make(map[resetter]struct{}), stopped: make(chan struct{})}
	s.model = newModel(cfg, now)
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

// resetAll breaks every live connection (a resetting outage began).
func (s *shaper) resetAll() {
	s.mu.Lock()
	conns := make([]resetter, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.reset()
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
func (s *shaper) beforeDial(ctx context.Context) error {
	s.model.firstUse()
	s.resetOnce.Do(func() {
		if times := s.model.resetTimes(); len(times) > 0 {
			go s.resetTimer(times)
		}
	})
	var timer *time.Timer
	for {
		resetting, wait, reset := s.model.dialState()
		s.handleReset(reset)
		if resetting {
			return errLinkReset
		}
		if wait == 0 {
			return nil
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
		}
	}
}

// resetTimer makes the resetting outages of the schedule happen on time
// even when no traffic passes (the rest of the model advances lazily, on
// the next unit of data).
func (s *shaper) resetTimer(times []int64) {
	timer := newStoppedTimer()
	for _, at := range times {
		if wait := at - s.now(); wait > 0 && !sleep(timer, time.Duration(wait), s.stopped, nil, nil) {
			return
		}
		s.handleReset(s.model.tick())
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

// wrapConn wraps what the carrier dialler returned for network.
func (s *shaper) wrapConn(network string, conn net.Conn) net.Conn {
	if network == "udp" {
		return newDatagramConn(s, conn)
	}
	return newStreamConn(s, conn)
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
