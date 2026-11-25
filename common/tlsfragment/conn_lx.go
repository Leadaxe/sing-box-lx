package tf

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// lx: SPEC 163 — the ACK wait of a fragmented ClientHello ends when the
// connection is closed and within a fixed budget.
//
// Upstream waits for the kernel to report every fragment acknowledged and
// listens only to the context of the TLS config, which lives as long as the
// box. On a path that accepts the TCP handshake and then drops data, the wait
// never ends: the handshake context expires, crypto/tls and uTLS close the
// connection, but the close of the socket blocks behind the polling loop that
// holds the descriptor, so nothing returns until the box is closed.

// ackWaitBudgetLx bounds the time the first Write may spend waiting for ACKs.
// Past it the remaining fragments go out without waiting; the handshake then
// fails or succeeds by its own deadline.
var ackWaitBudgetLx = 10 * time.Second

type ackWaitLx struct {
	initOnce  sync.Once
	closeOnce sync.Once
	closed    context.Context
	close     context.CancelFunc
	deadline  time.Time
}

func (w *ackWaitLx) init() {
	w.initOnce.Do(func() {
		w.closed, w.close = context.WithCancel(context.Background())
	})
}

func (c *Conn) writeAndWaitAckLx(payload []byte) error {
	c.lx.init()
	if c.lx.deadline.IsZero() {
		c.lx.deadline = time.Now().Add(ackWaitBudgetLx)
	}
	ctx, cancel := context.WithDeadline(c.ctx, c.lx.deadline)
	defer cancel()
	stop := context.AfterFunc(c.lx.closed, cancel)
	defer stop()
	err := writeAndWaitAck(ctx, c.tcpConn, payload, c.fallbackDelay)
	if err == nil || ctx.Err() == nil {
		return err
	}
	switch {
	case c.lx.closed.Err() != nil:
		return net.ErrClosed
	case c.ctx.Err() != nil:
		return c.ctx.Err()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The fragment is written; only the wait is given up.
		return nil
	default:
		return err
	}
}

// Close ends a pending ACK wait before closing the connection: the close of
// the socket would otherwise wait for the polling loop to release it.
func (c *Conn) Close() error {
	c.lx.init()
	c.lx.closeOnce.Do(c.lx.close)
	return c.Conn.Close()
}
