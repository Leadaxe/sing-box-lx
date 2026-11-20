package http

import (
	"context"
	"net"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// httpHandshakeDialerLx bounds only the HTTP CONNECT exchange (SPEC 157).
// TCP connect_timeout no longer applies once the proxy accepts the socket, and
// ordinary routed connections have no parent deadline. A proxy that never
// answers CONNECT would otherwise retain a connection and goroutine per retry.
//
// Start the watchdog after the underlying dial, without passing a new deadline
// into it: a detour may bind its entire stream/session to that context. Closing
// the connection also interrupts handshakes on transports without SetDeadline.
type httpHandshakeDialerLx struct {
	N.Dialer
}

func (d *httpHandshakeDialerLx) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return &httpHandshakeConnLx{
		Conn: conn,
		timer: time.AfterFunc(C.TCPTimeout, func() {
			_ = conn.Close()
		}),
	}, nil
}

type httpHandshakeConnLx struct {
	net.Conn
	timer *time.Timer
}

func (c *httpHandshakeConnLx) Close() error {
	c.timer.Stop()
	return c.Conn.Close()
}

func (c *httpHandshakeConnLx) Upstream() any {
	return c.Conn
}

func finishHTTPHandshakeLx(conn net.Conn, err error) (net.Conn, error) {
	if err != nil {
		// The HTTP client closes the connection on every handshake error. The
		// wrapper's Close releases the watchdog immediately, including for 503.
		return nil, err
	}
	// ReadResponse may have buffered tunnel bytes and wrapped the connection
	// in CachedConn. Preserve that buffer while finding the watchdog beneath it.
	if handshake, loaded := common.Cast[*httpHandshakeConnLx](conn); loaded {
		if !handshake.timer.Stop() {
			// Expiry won the race with a successful response; never hand a
			// connection to the caller while the watchdog is closing it.
			_ = conn.Close()
			return nil, context.DeadlineExceeded
		}
		if conn == handshake {
			return handshake.Conn, nil
		}
	}
	return conn, nil
}
