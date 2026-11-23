package dns

import (
	"net"
	"sync"
)

// SPEC 157. Stream DNS responses are written from per-query goroutines, and
// proxy server conns (VLESS, sing-mux) prepend their response header on the
// first Write behind an unsynchronized flag: concurrent first responses
// duplicate the header and the client reads a zero length prefix
// ("dns: short read"). Callers wrap the conn once per stream.
type serialWriteConn struct {
	net.Conn
	writeAccess sync.Mutex
}

func NewSerialWriteConn(conn net.Conn) net.Conn {
	return &serialWriteConn{Conn: conn}
}

func (c *serialWriteConn) Write(b []byte) (int, error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	return c.Conn.Write(b)
}

func (c *serialWriteConn) Upstream() any {
	return c.Conn
}
