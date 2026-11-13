package lxdatagram

// lx: SPEC 060 — quic.delay: the second Initial datagram of a QUIC handshake
// is held back.
//
// A sniffer that reassembles the ClientHello from the Initial packets it sees
// within a wait budget (Xray: 200 ms) gives up when the ClientHello spans two
// Initial datagrams and the second one arrives too late; it then forwards the
// flow to the dialled address. The server reassembles as usual. A ClientHello
// that fits one Initial leaves nothing to hold: the method does not apply to
// that node (QUICHandshake.Done withdraws the attempt's mark).
//
// The first flight is told from retransmissions by time: the QUIC stack sends
// its Initial datagrams back to back, while the earliest retransmission waits
// for a probe timeout of several initial RTTs. An Initial counts as the
// second one of the first flight only when it is the second Initial written,
// within firstFlightWindow of the first, and before the conn read anything.

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/logger"
)

// firstFlightWindow bounds the first flight: quic-go's first probe timeout is
// about three initial RTTs (100 ms each) away.
const firstFlightWindow = 100 * time.Millisecond

const (
	quicVersion1 = 0x00000001
	quicVersion2 = 0x6b3343cf
)

// isQUICInitial reports whether datagram starts with a QUIC Initial packet:
// a long header with the fixed bit set and the Initial type of QUIC v1 or v2
// (RFC 9000 §17.2.2, RFC 9369 §3.2).
func isQUICInitial(datagram []byte) bool {
	if len(datagram) < 5 || datagram[0]&0xc0 != 0xc0 {
		return false
	}
	packetType := (datagram[0] & 0x30) >> 4
	switch binary.BigEndian.Uint32(datagram[1:5]) {
	case quicVersion1:
		return packetType == 0
	case quicVersion2:
		return packetType == 1
	}
	return false
}

// InitialDelayConn holds the second Initial datagram of the first flight
// until hold has passed since the first one. A zero hold only watches: it
// tells whether the first flight had a second Initial.
type InitialDelayConn struct {
	net.Conn
	hold   time.Duration
	logger logger.Logger

	access   sync.Mutex
	initials int
	first    time.Time
	second   atomic.Bool
	read     atomic.Bool

	closeOnce sync.Once
	closed    chan struct{}
}

// NewInitialDelayConn wraps conn, the conn the QUIC stack writes its
// datagrams to one by one.
func NewInitialDelayConn(conn net.Conn, hold time.Duration, logger logger.Logger) *InitialDelayConn {
	return &InitialDelayConn{Conn: conn, hold: hold, logger: logger, closed: make(chan struct{})}
}

// SecondInitial reports whether the first flight had a second Initial
// datagram, that is whether the method had something to hold back.
func (c *InitialDelayConn) SecondInitial() bool {
	return c.second.Load()
}

func (c *InitialDelayConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && !c.read.Load() {
		c.read.Store(true)
	}
	return n, err
}

func (c *InitialDelayConn) Write(p []byte) (int, error) {
	if !isQUICInitial(p) {
		return c.Conn.Write(p)
	}
	now := time.Now()
	var wait time.Duration
	c.access.Lock()
	c.initials++
	switch {
	case c.initials == 1:
		c.first = now
	case c.initials == 2 && !c.read.Load() && now.Sub(c.first) < firstFlightWindow:
		c.second.Store(true)
		wait = c.hold - now.Sub(c.first)
	}
	c.access.Unlock()
	if wait > 0 {
		if c.logger != nil {
			c.logger.Debug("sniff guard: holding the second QUIC Initial (", len(p), " bytes) for ", wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-c.closed:
			timer.Stop()
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *InitialDelayConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *InitialDelayConn) Upstream() any {
	return c.Conn
}

// ReaderReplaceable is false: the first read closes the first flight.
func (c *InitialDelayConn) ReaderReplaceable() bool {
	return false
}

// WriterReplaceable is false: every write is inspected for Initials.
func (c *InitialDelayConn) WriterReplaceable() bool {
	return false
}
