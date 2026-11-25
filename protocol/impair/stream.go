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
	under net.Conn
	st    [2]streamState // guarded by sh.model.mu

	closed    chan struct{}
	closeOnce sync.Once
	resetErr  atomic.Bool

	// sending
	wmu      sync.Mutex // one Write at a time
	wtimer   *time.Timer
	upMu     sync.Mutex
	upQ      segQueue
	upBytes  int
	upFin    bool
	werr     error
	upSig    chan struct{} // to the writer: a segment was queued
	upSpace  chan struct{} // to Write: the line has room
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

func newStreamConn(s *shaper, under net.Conn) *streamConn {
	c := &streamConn{
		sh:       s,
		under:    under,
		closed:   make(chan struct{}),
		wtimer:   newStoppedTimer(),
		rtimer:   newStoppedTimer(),
		upSig:    make(chan struct{}, 1),
		upSpace:  make(chan struct{}, 1),
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
	if c.resetErr.Load() {
		return errLinkReset
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
			if isClosed(c.closed) {
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
			case <-c.closed:
			case <-c.wDead.wait():
			}
		}
		// the bottleneck
		var due int64
		for {
			var wait time.Duration
			var reset bool
			due, wait, reset = c.sh.model.admitStream(dirUp, &c.st[dirUp], n)
			c.sh.handleReset(reset)
			if wait == 0 {
				break
			}
			if !sleep(c.wtimer, wait, c.closed, c.wDead.wait(), nil) {
				if isClosed(c.closed) {
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
		down := c.sh.model.outage(dirUp)
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
			c.upMu.Unlock()
			if closer, ok := c.under.(interface{ CloseWrite() error }); ok {
				_ = closer.CloseWrite()
			}
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
		c.upMu.Unlock()
		signal(c.upSpace)
		if _, err := c.under.Write(scratch); err != nil {
			c.upMu.Lock()
			c.werr = err
			c.upMu.Unlock()
			signal(c.upSpace)
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
				due, wait, reset = c.sh.model.admitStream(dirDown, &c.st[dirDown], n)
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
		if isClosed(c.closed) {
			return 0, c.closedErr()
		}
		down := c.sh.model.outage(dirDown)
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
			if !sleep(c.rtimer, wait, c.closed, c.rDead.wait(), nil) && !isClosed(c.closed) {
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
		case <-c.closed:
		case <-c.rDead.wait():
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// ---- the rest of net.Conn ----

func (c *streamConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.under.Close()
		c.sh.unregister(c)
	})
	return err
}

func (c *streamConn) reset() {
	c.resetErr.Store(true)
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
