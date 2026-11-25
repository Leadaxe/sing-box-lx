package impair

import (
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Stream connections lose nothing. Sending: Write cuts the data into
// segments of segSize, admits each to the up bottleneck (a full queue or an
// outage makes Write wait: backpressure, the way a full send buffer blocks a
// TCP writer) and puts it into the connection's delay line; a writer
// goroutine sends what is due to the carrier, coalescing due segments into
// one write. Receiving: a reader goroutine reads the carrier into segments,
// admits each to the down bottleneck — waiting when the queue is full, and
// meanwhile not reading the carrier, so its receive window closes and the
// far sender slows down — and Read returns a segment only once it is due.
// Loss is a stall of the connection's delivery (model.admitStream).

// lineCap bounds the bytes held in one direction's delay line of one
// connection (in flight between admission and delivery): rate × delay.
const lineCap = 16 << 20

// flushLimit bounds how long Close waits for the writer to deliver what was
// written before it (a TCP close sends the send buffer too); an outage or a
// slow line longer than this loses the rest.
const flushLimit = 5 * time.Second

type seg struct {
	due int64
	bp  *[]byte
	n   int
	fin bool // CloseWrite marker
}

// segQueue is a FIFO ring of segments.
type segQueue struct {
	buf  []seg
	head int
	size int
}

func (q *segQueue) push(s seg) {
	if q.size == len(q.buf) {
		grown := make([]seg, max(16, 2*len(q.buf)))
		for i := 0; i < q.size; i++ {
			grown[i] = q.buf[(q.head+i)%len(q.buf)]
		}
		q.buf, q.head = grown, 0
	}
	q.buf[(q.head+q.size)%len(q.buf)] = s
	q.size++
}

func (q *segQueue) front() *seg { return &q.buf[q.head] }

func (q *segQueue) pop() {
	q.buf[q.head] = seg{}
	q.head = (q.head + 1) % len(q.buf)
	q.size--
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

type streamConn struct {
	sh    *shaper
	sub   *subline
	under net.Conn
	st    streamStates // guarded by sh.model.mu

	// uclosed closes on Close: Read and Write fail from then on. closed
	// closes when the carrier is closed: at once on a reset or the
	// outbound's close, else once the writer has delivered what was
	// written before Close (flush), in the background, as a TCP close
	// returns at once and the kernel sends the send buffer.
	uclosed   chan struct{}
	uOnce     sync.Once
	drainOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once
	resetErr  atomic.Pointer[error]

	// sending
	wmu      sync.Mutex // one Write at a time
	wtimer   *time.Timer
	upMu     sync.Mutex
	upQ      segQueue
	upBytes  int
	upFin    bool
	upBusy   bool // the writer is writing a batch to the carrier
	werr     error
	upSig    chan struct{} // to the writer: a segment was queued
	upSpace  chan struct{} // to Write: the line has room
	upIdle   chan struct{} // to Close: the writer finished a batch
	wDead    deadline
	upClosed chan struct{} // the writer goroutine has ended

	// receiving
	rmu     sync.Mutex // one Read at a time
	rtimer  *time.Timer
	rxMu    sync.Mutex
	rxQ     segQueue
	rxBytes int
	rxOff   int
	rerr    error
	rxSig   chan struct{} // to Read: a segment was queued or the carrier ended
	rxSpace chan struct{} // to the reader: the line has room
	rDead   deadline
}

func newStreamConn(s *shaper, under net.Conn, sub *subline) *streamConn {
	c := &streamConn{
		sh:       s,
		sub:      sub,
		under:    under,
		uclosed:  make(chan struct{}),
		closed:   make(chan struct{}),
		wtimer:   newStoppedTimer(),
		rtimer:   newStoppedTimer(),
		upSig:    make(chan struct{}, 1),
		upSpace:  make(chan struct{}, 1),
		upIdle:   make(chan struct{}, 1),
		rxSig:    make(chan struct{}, 1),
		rxSpace:  make(chan struct{}, 1),
		wDead:    makeDeadline(),
		rDead:    makeDeadline(),
		upClosed: make(chan struct{}),
	}
	if !s.register(c) {
		_ = c.Close()
		close(c.upClosed)
		return c
	}
	go c.writeLoop()
	go c.readLoop()
	return c
}

func (c *streamConn) closedErr() error {
	if err := c.resetErr.Load(); err != nil {
		return *err
	}
	return net.ErrClosed
}

// ---- sending ----

func (c *streamConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	total := 0
	for len(p) > 0 {
		n := min(len(p), segSize)
		// room in the delay line
		for {
			if isClosed(c.uclosed) {
				return total, c.closedErr()
			}
			if isClosed(c.wDead.wait()) {
				return total, os.ErrDeadlineExceeded
			}
			c.upMu.Lock()
			err, fin, full := c.werr, c.upFin, c.upBytes >= lineCap
			c.upMu.Unlock()
			if err != nil {
				return total, err
			}
			if fin {
				return total, net.ErrClosed
			}
			if !full {
				break
			}
			select {
			case <-c.upSpace:
			case <-c.uclosed:
			case <-c.wDead.wait():
			}
		}
		// the bottleneck
		var due int64
		for {
			var wait time.Duration
			var reset bool
			due, wait, reset = c.sh.model.admitStream(c.sub, dirUp, &c.st, n)
			c.sh.handleReset(reset)
			if wait == 0 {
				break
			}
			if !sleep(c.wtimer, wait, c.uclosed, c.wDead.wait(), nil) {
				if isClosed(c.uclosed) {
					return total, c.closedErr()
				}
				return total, os.ErrDeadlineExceeded
			}
		}
		bp := segPool.Get().(*[]byte)
		copy(*bp, p[:n])
		c.upMu.Lock()
		c.upQ.push(seg{due: due, bp: bp, n: n})
		c.upBytes += n
		c.upMu.Unlock()
		signal(c.upSig)
		p = p[n:]
		total += n
	}
	return total, nil
}

// CloseWrite half-closes after everything written so far is delivered.
func (c *streamConn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.upMu.Lock()
	if !c.upFin {
		c.upFin = true
		c.upQ.push(seg{fin: true})
	}
	c.upMu.Unlock()
	signal(c.upSig)
	return nil
}

func (c *streamConn) writeLoop() {
	defer close(c.upClosed)
	timer := newStoppedTimer()
	scratch := make([]byte, 0, 64<<10)
	for {
		if isClosed(c.closed) {
			return
		}
		down := c.sh.model.outage(c.sub, dirUp)
		c.upMu.Lock()
		if c.upQ.size == 0 {
			c.upMu.Unlock()
			select {
			case <-c.upSig:
			case <-c.closed:
			}
			continue
		}
		head := c.upQ.front()
		if now := c.sh.now(); head.due > now {
			wait := time.Duration(head.due - now)
			c.upMu.Unlock()
			sleep(timer, wait, c.closed, nil, nil)
			continue
		}
		if down {
			// a frozen link holds what is in flight
			c.upMu.Unlock()
			sleep(timer, outagePoll, c.closed, nil, nil)
			continue
		}
		if head.fin {
			c.upQ.pop()
			c.upBusy = true
			c.upMu.Unlock()
			if closer, ok := c.under.(interface{ CloseWrite() error }); ok {
				_ = closer.CloseWrite()
			}
			c.upMu.Lock()
			c.upBusy = false
			c.upMu.Unlock()
			signal(c.upIdle)
			continue
		}
		now := c.sh.now()
		scratch = scratch[:0]
		for c.upQ.size > 0 {
			s := c.upQ.front()
			if s.fin || s.due > now || len(scratch)+s.n > cap(scratch) {
				break
			}
			scratch = append(scratch, (*s.bp)[:s.n]...)
			segPool.Put(s.bp)
			c.upBytes -= s.n
			c.upQ.pop()
		}
		c.upBusy = true
		c.upMu.Unlock()
		signal(c.upSpace)
		_, err := c.under.Write(scratch)
		c.upMu.Lock()
		c.upBusy = false
		if err != nil {
			c.werr = err
		}
		c.upMu.Unlock()
		signal(c.upIdle)
		if err != nil {
			signal(c.upSpace)
			return
		}
	}
}

// flush waits, up to flushLimit, until the writer has delivered everything
// queued by Write and CloseWrite. Not on a reset or the outbound's close.
func (c *streamConn) flush() {
	limit := time.NewTimer(flushLimit)
	defer limit.Stop()
	for {
		if c.resetErr.Load() != nil || isClosed(c.sh.stopped) {
			return
		}
		c.upMu.Lock()
		done := c.upQ.size == 0 && !c.upBusy || c.werr != nil
		c.upMu.Unlock()
		if done {
			return
		}
		select {
		case <-c.upIdle:
		case <-c.upClosed:
			return
		case <-c.sh.stopped:
			return
		case <-limit.C:
			return
		}
	}
}

// ---- receiving ----

func (c *streamConn) readLoop() {
	timer := newStoppedTimer()
	fail := func(err error) {
		c.rxMu.Lock()
		c.rerr = err
		c.rxMu.Unlock()
		signal(c.rxSig)
	}
	for {
		for {
			c.rxMu.Lock()
			full := c.rxBytes >= lineCap
			c.rxMu.Unlock()
			if !full {
				break
			}
			select {
			case <-c.rxSpace:
			case <-c.closed:
				return
			}
		}
		bp := segPool.Get().(*[]byte)
		n, err := c.under.Read(*bp)
		if n > 0 {
			var due int64
			for {
				var wait time.Duration
				var reset bool
				due, wait, reset = c.sh.model.admitStream(c.sub, dirDown, &c.st, n)
				c.sh.handleReset(reset)
				if wait == 0 {
					break
				}
				if !sleep(timer, wait, c.closed, nil, nil) {
					segPool.Put(bp)
					return
				}
			}
			c.rxMu.Lock()
			c.rxQ.push(seg{due: due, bp: bp, n: n})
			c.rxBytes += n
			c.rxMu.Unlock()
			signal(c.rxSig)
		} else {
			segPool.Put(bp)
		}
		if err != nil {
			fail(err)
			return
		}
	}
}

func (c *streamConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if isClosed(c.uclosed) {
			return 0, c.closedErr()
		}
		down := c.sh.model.outage(c.sub, dirDown)
		c.rxMu.Lock()
		if c.rxQ.size > 0 {
			now := c.sh.now()
			head := c.rxQ.front()
			if head.due <= now && !down {
				total := 0
				for c.rxQ.size > 0 && total < len(p) {
					s := c.rxQ.front()
					if s.due > now {
						break
					}
					k := copy(p[total:], (*s.bp)[c.rxOff:s.n])
					total += k
					c.rxOff += k
					c.rxBytes -= k
					if c.rxOff == s.n {
						segPool.Put(s.bp)
						c.rxQ.pop()
						c.rxOff = 0
					}
				}
				c.rxMu.Unlock()
				signal(c.rxSpace)
				return total, nil
			}
			wait := outagePoll
			if !down {
				wait = time.Duration(head.due - now)
			}
			c.rxMu.Unlock()
			if !sleep(c.rtimer, wait, c.uclosed, c.rDead.wait(), nil) && !isClosed(c.uclosed) {
				return 0, os.ErrDeadlineExceeded
			}
			continue
		}
		if err := c.rerr; err != nil {
			c.rxMu.Unlock()
			if err == io.EOF {
				return 0, io.EOF
			}
			return 0, err
		}
		c.rxMu.Unlock()
		select {
		case <-c.rxSig:
		case <-c.uclosed:
		case <-c.rDead.wait():
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// ---- the rest of net.Conn ----

func (c *streamConn) Close() error {
	c.uOnce.Do(func() { close(c.uclosed) })
	if c.resetErr.Load() != nil || isClosed(c.sh.stopped) {
		return c.closeCarrier()
	}
	c.drainOnce.Do(func() {
		go func() {
			c.flush()
			_ = c.closeCarrier()
		}()
	})
	return nil
}

// closeCarrier ends the connection's goroutines and closes the carrier.
func (c *streamConn) closeCarrier() error {
	var err error
	c.closeOnce.Do(func() {
		c.uOnce.Do(func() { close(c.uclosed) })
		close(c.closed)
		err = c.under.Close()
		c.sh.unregister(c)
	})
	return err
}

func (c *streamConn) reset(err error) {
	c.resetErr.Store(&err)
	_ = c.Close()
}

func (c *streamConn) LocalAddr() net.Addr  { return c.under.LocalAddr() }
func (c *streamConn) RemoteAddr() net.Addr { return c.under.RemoteAddr() }

func (c *streamConn) SetDeadline(t time.Time) error {
	c.rDead.set(t)
	c.wDead.set(t)
	return nil
}

func (c *streamConn) SetReadDeadline(t time.Time) error {
	c.rDead.set(t)
	return nil
}

func (c *streamConn) SetWriteDeadline(t time.Time) error {
	c.wDead.set(t)
	return nil
}
