package impair

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Datagram connections. Sending: WriteTo admits the datagram to the up
// bottleneck (drop = it is silently gone, as on a real link); at its due
// time the up pump hands it to the connection's own writer goroutine, which
// writes it to the carrier, so a carrier that blocks holds back only its own
// connection. Receiving: a reader goroutine
// takes every datagram off the carrier as it arrives, admits it to the down
// bottleneck (drop = gone, like a policer with a queue in front of it) and
// the down pump hands it to ReadFrom at its due time.

const rxQueue = 1024 // datagrams delivered but not yet read; beyond it they are dropped (a full socket buffer)

const txQueue = 1024 // datagrams due but not yet written to the carrier; beyond it they are dropped (a full send buffer)

type rxItem struct {
	bp   *[]byte
	n    int
	addr net.Addr
}

type packetCore struct {
	sh    *shaper
	sub   *subline
	under net.PacketConn

	rx        chan rxItem
	tx        chan rxItem
	rdone     chan struct{}
	rerr      error
	closed    chan struct{}
	closeOnce sync.Once
	resetErr  atomic.Pointer[error]
	werr      atomic.Pointer[error]

	readDeadline deadline
}

func newPacketCore(s *shaper, under net.PacketConn, sub *subline) *packetCore {
	c := &packetCore{
		sh:           s,
		sub:          sub,
		under:        under,
		rx:           make(chan rxItem, rxQueue),
		tx:           make(chan rxItem, txQueue),
		rdone:        make(chan struct{}),
		closed:       make(chan struct{}),
		readDeadline: makeDeadline(),
	}
	if !s.register(c) {
		_ = under.Close()
		close(c.closed)
		close(c.rdone)
		c.rerr = net.ErrClosed
		return c
	}
	go c.readLoop()
	go c.writeLoop()
	return c
}

func (c *packetCore) readLoop() {
	buf := make([]byte, 65535)
	defer close(c.rdone)
	for {
		n, addr, err := c.under.ReadFrom(buf)
		if n > 0 {
			due, v, reset := c.sh.model.admitPacket(c.sub, dirDown, n)
			c.sh.handleReset(reset)
			if v == verdictPass {
				bp := getPkt(n)
				copy(*bp, buf[:n])
				c.sh.pumps[dirDown].push(pktEntry{due: due, pc: c, bp: bp, n: n, addr: addr})
			}
		}
		if err != nil {
			c.rerr = err
			return
		}
	}
}

// deliverIn is the down pump handing over a datagram.
func (c *packetCore) deliverIn(bp *[]byte, n int, addr net.Addr) {
	select {
	case <-c.closed:
		putPkt(bp)
		return
	default:
	}
	select {
	case c.rx <- rxItem{bp: bp, n: n, addr: addr}:
	default:
		putPkt(bp)
	}
}

// sendOut is the up pump handing over a datagram that is due: it never
// blocks the pump, which serves every connection of the outbound.
func (c *packetCore) sendOut(bp *[]byte, n int, addr net.Addr) {
	if isClosed(c.closed) {
		putPkt(bp)
		return
	}
	select {
	case c.tx <- rxItem{bp: bp, n: n, addr: addr}:
	default:
		putPkt(bp)
	}
}

// writeLoop writes the due datagrams of this connection to the carrier.
func (c *packetCore) writeLoop() {
	for {
		select {
		case item := <-c.tx:
			if !isClosed(c.closed) {
				if _, err := c.under.WriteTo((*item.bp)[:item.n], item.addr); err != nil {
					c.storeWriteErr(err)
				}
			}
			putPkt(item.bp)
		case <-c.closed:
			return
		}
	}
}

// storeWriteErr keeps a carrier write error for the next WriteTo. Its own
// function, so the error moves to the heap only when there is one: taking
// its address in writeLoop made every datagram allocate.
func (c *packetCore) storeWriteErr(err error) {
	c.werr.Store(&err)
}

func (c *packetCore) closedErr() error {
	if err := c.resetErr.Load(); err != nil {
		return *err
	}
	return net.ErrClosed
}

func (c *packetCore) readFrom(p []byte) (int, net.Addr, error) {
	take := func(item rxItem) (int, net.Addr, error) {
		n := copy(p, (*item.bp)[:item.n])
		putPkt(item.bp)
		return n, item.addr, nil
	}
	select {
	case item := <-c.rx:
		return take(item)
	default:
	}
	select {
	case item := <-c.rx:
		return take(item)
	case <-c.closed:
		return 0, nil, c.closedErr()
	case <-c.readDeadline.wait():
		return 0, nil, os.ErrDeadlineExceeded
	case <-c.rdone:
		// the carrier ended: what is still in the delay line is lost with it
		select {
		case item := <-c.rx:
			return take(item)
		default:
		}
		return 0, nil, c.rerr
	}
}

func (c *packetCore) writeTo(p []byte, addr net.Addr) (int, error) {
	if isClosed(c.closed) {
		return 0, c.closedErr()
	}
	if errp := c.werr.Swap(nil); errp != nil {
		return 0, *errp
	}
	due, v, reset := c.sh.model.admitPacket(c.sub, dirUp, len(p))
	c.sh.handleReset(reset)
	if v != verdictPass {
		return len(p), nil
	}
	bp := getPkt(len(p))
	copy(*bp, p)
	c.sh.pumps[dirUp].push(pktEntry{due: due, pc: c, bp: bp, n: len(p), addr: addr})
	return len(p), nil
}

func (c *packetCore) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.under.Close()
		c.sh.unregister(c)
	})
	return err
}

func (c *packetCore) reset(err error) {
	c.resetErr.Store(&err)
	_ = c.Close()
}

// packetConn is the net.PacketConn of ListenPacket.
type packetConn struct {
	*packetCore
}

func newPacketConn(s *shaper, under net.PacketConn, sub *subline) *packetConn {
	return &packetConn{newPacketCore(s, under, sub)}
}

func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) { return c.readFrom(p) }
func (c *packetConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return c.writeTo(p, addr)
}
func (c *packetConn) LocalAddr() net.Addr { return c.under.LocalAddr() }
func (c *packetConn) SetDeadline(t time.Time) error {
	c.readDeadline.set(t)
	return c.under.SetWriteDeadline(t)
}

func (c *packetConn) SetReadDeadline(t time.Time) error {
	c.readDeadline.set(t)
	return nil
}
func (c *packetConn) SetWriteDeadline(t time.Time) error { return c.under.SetWriteDeadline(t) }

// datagramConn is the connected net.Conn of DialContext("udp"): every Read
// and Write is one datagram.
type datagramConn struct {
	*packetCore
	conn net.Conn
}

// connPacket adapts a connected datagram net.Conn to the PacketConn the
// core works on.
type connPacket struct {
	net.Conn
}

func (c connPacket) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(p)
	return n, c.Conn.RemoteAddr(), err
}

func (c connPacket) WriteTo(p []byte, _ net.Addr) (int, error) { return c.Conn.Write(p) }

func newDatagramConn(s *shaper, conn net.Conn, sub *subline) *datagramConn {
	return &datagramConn{packetCore: newPacketCore(s, connPacket{conn}, sub), conn: conn}
}

func (c *datagramConn) Read(p []byte) (int, error) {
	n, _, err := c.readFrom(p)
	return n, err
}
func (c *datagramConn) Write(p []byte) (int, error) { return c.writeTo(p, c.conn.RemoteAddr()) }
func (c *datagramConn) LocalAddr() net.Addr         { return c.conn.LocalAddr() }
func (c *datagramConn) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }
func (c *datagramConn) SetDeadline(t time.Time) error {
	c.readDeadline.set(t)
	return c.conn.SetWriteDeadline(t)
}

func (c *datagramConn) SetReadDeadline(t time.Time) error {
	c.readDeadline.set(t)
	return nil
}
func (c *datagramConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
