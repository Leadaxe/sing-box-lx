package http

// lx: SPEC 162 — notice a client that leaves while a plain HTTP/1.x request
// waits for the upstream's response.
//
// Once the request has been written to the upstream in full, nobody reads the
// client connection until the response comes back, so a client that closes its
// connection goes unnoticed and the connection through the node stays open for
// as long as the site keeps silent. net/http's server solves the same with a
// background read (connReader.startBackgroundRead); this is that read on top of
// the buffered reader the proxy already has: a Peek(1) that consumes nothing.
//
//   - a byte arrives: the next request (pipelining), the client is alive; the
//     byte stays in the buffer and is served in order;
//   - a timeout: our own stop, or a deadline someone else set; nothing to do;
//   - EOF or a reset: the client is gone; both ends of the pipe to the upstream
//     are closed, so the routed connection is torn down instead of getting only
//     a half-close.
//
// A connection that does not take read deadlines is served as upstream does,
// without the watch.

import (
	std_bufio "bufio"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// forwardWatchStopTimeout bounds how long stop waits for the background read
// once its deadline is in the past; a connection that accepts deadlines but
// does not honour them is closed after it.
const forwardWatchStopTimeout = time.Second

type forwardWatch struct {
	conn     net.Conn
	reader   *std_bufio.Reader
	upstream *upstreamConn

	access  sync.Mutex
	started bool
	stopped bool
	done    chan struct{}
	gone    atomic.Bool
}

func newForwardWatch(conn net.Conn, reader *std_bufio.Reader, upstream *upstreamConn) *forwardWatch {
	return &forwardWatch{conn: conn, reader: reader, upstream: upstream, done: make(chan struct{})}
}

// start begins the background read. It is called once the request has been
// written to the upstream in full: the request body is read, so nothing else
// reads the client connection until the response is written.
func (w *forwardWatch) start() {
	w.access.Lock()
	defer w.access.Unlock()
	if w.started || w.stopped {
		return
	}
	if w.conn.SetReadDeadline(time.Time{}) != nil {
		return
	}
	w.started = true
	go w.loop()
}

func (w *forwardWatch) loop() {
	defer close(w.done)
	_, err := w.reader.Peek(1)
	if err == nil || isTimeout(err) {
		return
	}
	w.gone.Store(true)
	w.upstream.closeHard()
}

// clientGone reports whether the client left while the response was awaited.
func (w *forwardWatch) clientGone() bool {
	return w.gone.Load()
}

// stop ends the background read and waits for it. The reader returns the
// timeout once and stays usable for the next request.
func (w *forwardWatch) stop() {
	w.access.Lock()
	defer w.access.Unlock()
	if w.stopped {
		return
	}
	w.stopped = true
	if !w.started {
		return
	}
	w.conn.SetReadDeadline(time.Now())
	select {
	case <-w.done:
	case <-time.After(forwardWatchStopTimeout):
		w.conn.Close()
		<-w.done
	}
	w.conn.SetReadDeadline(time.Time{})
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// closeHard closes both ends of the pipe to the upstream: the routed copy then
// sees a broken connection, not only the end of the stream it would answer
// with a half-close.
func (c *upstreamConn) closeHard() {
	c.Close()
	if c.serverSide != nil {
		c.serverSide.Close()
	}
}
