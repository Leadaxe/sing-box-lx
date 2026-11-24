// Package lxhandshake bounds the synchronous proxy handshake an outbound runs
// inside DialContext, after its server connection is already open.
//
// The connection manager dials without a deadline, and the TCP connect timeout
// ends once the server accepts the socket. A server that accepts and then says
// nothing (a stopped process, a stuck ACL helper) parks the handshake read
// forever: nothing reaches it, because the outbound has not handed a conn up yet
// and the inbound side does not cancel the dial context when its client leaves.
// Every retry adds a goroutine and a socket.
//
// Two forms, both wrappers around the library client rather than copies of it:
//
//   - Context + Dialer, for clients that already honour ctx during the
//     handshake (sing's SOCKS client). The returned context is cancelled by a
//     timer started after the client's underlying dial returns, while the
//     underlying dial itself keeps the caller's context: a detour may bind its
//     session lifetime to the dial context, and no new deadline is passed into
//     it. The client resolves the race between its result and the timer itself.
//   - Guard, for exchanges that take no context at all: a deadline on the conn
//     plus closing it when ctx ends, both removed once the exchange returns.
//
// Neither form changes a successful connection: the timer, the deadline and the
// context watch are gone before DialContext returns.
package lxhandshake

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Timeout is the ceiling of a handshake whose caller set no earlier deadline;
// a variable so that tests can shorten it.
var Timeout = C.TCPTimeout

const guardCloseGrace = time.Second

// ErrTimeout is returned when the handshake, not the caller, ran out of time.
// It matches os.ErrDeadlineExceeded and reports Timeout() like a net.Error.
var ErrTimeout error = timeoutError{}

type timeoutError struct{}

func (timeoutError) Error() string   { return "handshake timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Unwrap() error   { return os.ErrDeadlineExceeded }

type stateKey struct{}

// handshakeContext is cancelled by the parent, by the handshake timer, or by
// the cancel func returned from Context. Err reports ErrTimeout for the timer,
// so a client that returns ctx.Err() reports a timeout, not a cancellation.
type handshakeContext struct {
	context.Context
	parent context.Context
	cancel context.CancelCauseFunc
	access sync.Mutex
	timer  *time.Timer
	ended  bool
}

func (c *handshakeContext) Err() error {
	err := c.Context.Err()
	if err != nil && context.Cause(c.Context) == ErrTimeout {
		return ErrTimeout
	}
	return err
}

func (c *handshakeContext) Value(key any) any {
	if key == (stateKey{}) {
		return c
	}
	return c.Context.Value(key)
}

// startTimer arms the handshake timer once, when the client's server
// connection is open; later dials of the same attempt share its budget.
func (c *handshakeContext) startTimer() {
	c.access.Lock()
	defer c.access.Unlock()
	if c.ended || c.timer != nil {
		return
	}
	c.timer = time.AfterFunc(Timeout, func() {
		c.cancel(ErrTimeout)
	})
}

func (c *handshakeContext) end() {
	c.access.Lock()
	c.ended = true
	if c.timer != nil {
		c.timer.Stop()
		// The parent may outlive the attempt; do not keep the timer reachable.
		c.timer = nil
	}
	c.access.Unlock()
	c.cancel(context.Canceled)
}

// Context returns the context to hand to a client whose dialer is wrapped by
// NewDialer. The cancel func must run when the client call returns.
func Context(ctx context.Context) (context.Context, context.CancelFunc) {
	inner, cancel := context.WithCancelCause(ctx)
	handshake := &handshakeContext{Context: inner, parent: ctx, cancel: cancel}
	return handshake, handshake.end
}

// Dialer wraps the dialer a protocol client uses for its own server
// connections. Called under a context from Context, it dials with the caller's
// original context and arms the handshake timer after a TCP dial succeeds;
// called under any other context it behaves exactly like the wrapped dialer.
type Dialer struct {
	N.Dialer
}

func NewDialer(dialer N.Dialer) *Dialer {
	return &Dialer{Dialer: dialer}
}

func (d *Dialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	handshake, loaded := ctx.Value(stateKey{}).(*handshakeContext)
	if !loaded {
		return d.Dialer.DialContext(ctx, network, destination)
	}
	conn, err := d.Dialer.DialContext(handshake.parent, network, destination)
	if err == nil && N.NetworkName(network) == N.NetworkTCP {
		handshake.startTimer()
	}
	return conn, err
}

func (d *Dialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if handshake, loaded := ctx.Value(stateKey{}).(*handshakeContext); loaded {
		ctx = handshake.parent
	}
	return d.Dialer.ListenPacket(ctx, destination)
}

func (d *Dialer) Upstream() any {
	return d.Dialer
}

// Guard bounds a synchronous exchange on conn that takes no context: reads and
// writes get a deadline of Timeout (or the earlier deadline of ctx), and conn is
// closed when ctx ends or, as a backstop for conns whose deadline does not work,
// shortly after the deadline. The returned func removes all three and must run as
// soon as the exchange returns, before conn is handed to anyone else. It reports
// an error when the context watch or the backstop fired before it could stop them:
// conn is closed or being closed then, however the exchange ended, and the caller
// must not hand it out — ctx's error for the context, ErrTimeout for the backstop
// (as sing's SOCKS client returns for a handshake that lost the race to its
// context). Calling it again returns the same result.
func Guard(ctx context.Context, conn net.Conn) (finish func() error) {
	deadline := time.Now().Add(Timeout)
	if ctxDeadline, loaded := ctx.Deadline(); loaded && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	deadlineSet := conn.SetDeadline(deadline) == nil
	closeAt := time.Until(deadline)
	if deadlineSet {
		// The deadline fails the exchange first; closing is the backstop for a
		// conn that accepts SetDeadline without enforcing it.
		closeAt += guardCloseGrace
	}
	timer := time.AfterFunc(closeAt, func() {
		_ = conn.Close()
	})
	stopContext := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	var (
		once sync.Once
		err  error
	)
	return func() error {
		once.Do(func() {
			contextStopped := stopContext()
			timerStopped := timer.Stop()
			if deadlineSet {
				_ = conn.SetDeadline(time.Time{})
			}
			switch {
			case !contextStopped:
				err = ctx.Err()
			case !timerStopped:
				err = ErrTimeout
			}
		})
		return err
	}
}
