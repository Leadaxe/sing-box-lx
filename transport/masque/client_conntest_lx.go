package masque

// lx: SPEC 139 — what a connection test reports about the tunnel a dial goes
// through: when the current session was opened and on which HTTP version.

import (
	"sync/atomic"
	"time"
)

type tunnelOpenLx struct {
	at        time.Time
	datagrams bool
}

type tunnelStateLx struct {
	current atomic.Pointer[tunnelOpenLx]
}

func (s *tunnelStateLx) opened(datagrams bool) {
	s.current.Store(&tunnelOpenLx{at: time.Now(), datagrams: datagrams})
}

// TunnelInfoLx reports the age of the last opened session and the HTTP version
// carrying it: "h3" when IP packets travel as QUIC datagrams, "h2" for a
// stream transport. ok is false before the first session.
func (c *Client) TunnelInfoLx() (age time.Duration, vhttp string, ok bool) {
	open := c.lxTunnel.current.Load()
	if open == nil {
		return 0, "", false
	}
	vhttp = "h2"
	if open.datagrams {
		vhttp = "h3"
	}
	return time.Since(open.at), vhttp, true
}
