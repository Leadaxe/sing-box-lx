package impair

import (
	"net"
	"sync"
	"time"
)

// Buffers: datagrams up to pktBufSize and stream segments (segSize) come
// from pools, so the steady state allocates nothing per unit; a datagram
// above pktBufSize gets its own buffer.
const (
	pktBufSize = 2048
	segSize    = 4096
)

var (
	pktPool = sync.Pool{New: func() any { b := make([]byte, pktBufSize); return &b }}
	segPool = sync.Pool{New: func() any { b := make([]byte, segSize); return &b }}
)

func getPkt(n int) *[]byte {
	if n <= pktBufSize {
		return pktPool.Get().(*[]byte)
	}
	b := make([]byte, n)
	return &b
}

func putPkt(bp *[]byte) {
	if cap(*bp) == pktBufSize {
		pktPool.Put(bp)
	}
}

// pumpSlack: a wake delivers everything due within this much of now, so
// the timer fires about once per slack at high packet rates instead of once
// per packet.
const pumpSlack = 250 * time.Microsecond

type pktEntry struct {
	due  int64
	seq  uint64
	pc   *packetCore
	bp   *[]byte
	n    int
	addr net.Addr
}

// pump is the delay line of one direction's datagrams for every packet
// connection of the outbound: a min-heap on (due, seq) and one goroutine
// with one reusable timer. Up: hands the datagram to its connection's
// writer at the due time.
// Down: hands the datagram to its connection's reader.
type pump struct {
	dir  int
	now  func() int64
	mu   sync.Mutex
	heap []pktEntry
	seq  uint64
	wake chan struct{}
	done chan struct{}
	once sync.Once
	stop sync.Once
}

func newPump(dir int, now func() int64) *pump {
	return &pump{dir: dir, now: now, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (p *pump) less(i, j int) bool {
	a, b := &p.heap[i], &p.heap[j]
	return a.due < b.due || a.due == b.due && a.seq < b.seq
}

func (p *pump) push(e pktEntry) {
	p.once.Do(func() { go p.run() })
	p.mu.Lock()
	select {
	case <-p.done:
		p.mu.Unlock()
		putPkt(e.bp)
		return
	default:
	}
	e.seq = p.seq
	p.seq++
	p.heap = append(p.heap, e)
	i := len(p.heap) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !p.less(i, parent) {
			break
		}
		p.heap[i], p.heap[parent] = p.heap[parent], p.heap[i]
		i = parent
	}
	top := i == 0
	p.mu.Unlock()
	if top {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *pump) pop() pktEntry {
	top := p.heap[0]
	last := len(p.heap) - 1
	p.heap[0] = p.heap[last]
	p.heap[last] = pktEntry{}
	p.heap = p.heap[:last]
	i := 0
	for {
		l, r, smallest := 2*i+1, 2*i+2, i
		if l < last && p.less(l, smallest) {
			smallest = l
		}
		if r < last && p.less(r, smallest) {
			smallest = r
		}
		if smallest == i {
			break
		}
		p.heap[i], p.heap[smallest] = p.heap[smallest], p.heap[i]
		i = smallest
	}
	return top
}

func (p *pump) run() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var batch []pktEntry
	for {
		p.mu.Lock()
		now := p.now()
		for len(p.heap) > 0 && p.heap[0].due <= now+int64(pumpSlack) {
			batch = append(batch, p.pop())
		}
		next := int64(-1)
		if len(p.heap) > 0 {
			next = p.heap[0].due
		}
		p.mu.Unlock()
		for i := range batch {
			p.deliver(&batch[i])
			batch[i] = pktEntry{}
		}
		batch = batch[:0]
		if next >= 0 {
			timer.Reset(time.Duration(next - now))
			select {
			case <-timer.C:
			case <-p.wake:
				timer.Stop()
			case <-p.done:
				timer.Stop()
				p.drain()
				return
			}
		} else {
			select {
			case <-p.wake:
			case <-p.done:
				p.drain()
				return
			}
		}
	}
}

func (p *pump) deliver(e *pktEntry) {
	if p.dir == dirUp {
		e.pc.sendOut(e.bp, e.n, e.addr)
		return
	}
	e.pc.deliverIn(e.bp, e.n, e.addr)
}

func (p *pump) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.heap)
}

func (p *pump) close() {
	p.stop.Do(func() {
		p.mu.Lock()
		close(p.done)
		p.mu.Unlock()
		p.once.Do(func() {}) // never started: nothing to drain
		p.drain()
	})
}

func (p *pump) drain() {
	p.mu.Lock()
	for len(p.heap) > 0 {
		e := p.pop()
		putPkt(e.bp)
	}
	p.mu.Unlock()
}
