package http

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sHTTP "github.com/sagernet/sing/protocol/http"
)

var httpConnectHandshakeTimeoutLx = C.TCPTimeout

type httpHandshakeStateKeyLx struct{}

type httpHandshakeStateLx struct {
	// The watchdog and the caller compete to finish each CONNECT attempt.
	finished atomic.Bool
	timer    *time.Timer
}

// httpHandshakeDialerLx bounds only the HTTP CONNECT exchange (SPEC 161).
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
	handshake, loaded := ctx.Value(httpHandshakeStateKeyLx{}).(*httpHandshakeStateLx)
	if !loaded {
		// Not dialed through dialHTTPWithHandshakeTimeoutLx: behave as upstream.
		return conn, nil
	}
	handshake.timer = time.AfterFunc(httpConnectHandshakeTimeoutLx, func() {
		if !handshake.finished.Swap(true) {
			_ = conn.Close()
		}
	})
	return conn, nil
}

func dialHTTPWithHandshakeTimeoutLx(ctx context.Context, client *sHTTP.Client, network string, destination M.Socksaddr) (net.Conn, error) {
	var handshake httpHandshakeStateLx
	conn, err := client.DialContext(context.WithValue(ctx, httpHandshakeStateKeyLx{}, &handshake), network, destination)
	expired := handshake.finished.Swap(true)
	if handshake.timer != nil {
		handshake.timer.Stop()
		// A detour can retain the dial context for its session lifetime.
		handshake.timer = nil
	}
	if !expired {
		// Preserve CachedConn and any tunnel data buffered with the response.
		return conn, err
	}
	// Expiry won, even if a response arrived while the watchdog was closing
	// the socket. Close any returned connection to release buffered data too.
	if conn != nil {
		_ = conn.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("CONNECT handshake timeout: %w", os.ErrDeadlineExceeded)
}
