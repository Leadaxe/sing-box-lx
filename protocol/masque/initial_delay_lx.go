package masque

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
)

// lx: prototype (2026-11-02, owner's go-ahead) — hold back the second QUIC
// Initial datagram of a fresh h3 dial so that an Xray relay with `sniffing`
// (`destOverride` without `routeOnly`) cannot reassemble the ClientHello
// within its 200 ms sniff budget. The relay then gives up and forwards the
// cached first packet to the original address; the second packet follows and
// the server reassembles as usual. Enabled only by LX_H3_INITIAL_DELAY (a
// duration, e.g. 250ms); the config form is the owner's call.
//
// Only long-header Initial packets (first byte 0b1100xxxx, QUIC v1) of the
// first `delay` window after the first write are held; retransmissions past
// the window and everything else pass through untouched.
type initialDelayConn struct {
	net.Conn
	delay  time.Duration
	logger log.ContextLogger

	mu    sync.Mutex
	first time.Time
	held  int
}

func initialDelayFromEnv() time.Duration {
	v := os.Getenv("LX_H3_INITIAL_DELAY")
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func wrapInitialDelay(conn net.Conn, delay time.Duration, logger log.ContextLogger) net.Conn {
	if delay <= 0 {
		return conn
	}
	return &initialDelayConn{Conn: conn, delay: delay, logger: logger}
}

func isQUICv1Initial(p []byte) bool {
	return len(p) >= 5 && p[0]&0xf0 == 0xc0 && p[1] == 0 && p[2] == 0 && p[3] == 0 && p[4] == 1
}

func (c *initialDelayConn) Write(p []byte) (int, error) {
	if isQUICv1Initial(p) {
		c.mu.Lock()
		if c.first.IsZero() {
			c.first = time.Now()
			c.mu.Unlock()
			return c.Conn.Write(p)
		}
		wait := c.delay - time.Since(c.first)
		if wait > 0 {
			c.held++
			n := c.held
			c.mu.Unlock()
			if c.logger != nil {
				c.logger.Debug("masque: holding Initial datagram #", n+1, " (", len(p), " bytes) for ", wait, " past the relay sniff budget")
			}
			time.Sleep(wait)
			return c.Conn.Write(p)
		}
		c.mu.Unlock()
	}
	return c.Conn.Write(p)
}
