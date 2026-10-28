package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"sync"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// lx: SPEC 141 — packet-up upload pipeline.
//
// Xray's packet-up client (transport/internet/splithttp/dialer.go) does not
// wait for an upload POST's response before sending the next one: writes are
// batched through a bounded pipe sized by sc_max_each_post_bytes, the next POST
// starts as soon as the previous request body has been written
// (httptrace.WroteRequest), and responses are collected in the background. The
// server reorders packets by seq (upload_queue.go, window
// sc_max_buffered_posts, default 30). Until this task every Write here was one
// POST whose response was awaited inline, so a stream uploaded one write per
// round trip (issue #58).
var (
	// packetUpMaxInflight caps the upload POSTs of one stream whose response is
	// still outstanding — the bound on buffered payloads (each is held for the
	// GetBody replay until its response ends) and on how far ahead of the
	// server's reorder window the client runs. Variable: tests shrink it.
	packetUpMaxInflight = 16
	// packetUpMinRate scales one POST's timeout with its payload
	// (packetUpPostTimeout + len/packetUpMinRate): a batched post is up to
	// sc_max_each_post_bytes, and a flat bound sized for a probe would fail a
	// healthy slow uplink on a full post. A wedged connection still costs one
	// bounded post, proportional to what was committed to it.
	packetUpMinRate = 16 * 1024 // bytes per second
)

// packetConn implements packet-up: download is a GET response body, uploads
// are sequenced POSTs to "<path>/<sessionId>/<seq>" issued by uploadLoop from
// a bounded batch buffer.
//
// The reader is late-bound: dialPacketUp hands the conn up before the download
// response exists (see the deadlock note there), so Read waits on `created`
// until the RoundTrip goroutine binds either a body or an error.
type packetConn struct {
	ctx    context.Context
	client *Client
	// xmux is the pooled connection carrying every upload POST of this stream;
	// released when the conn closes.
	xmux *xmuxRelease
	// breaker classifies read results and local teardown (lx: SPEC 076).
	breaker    connBreaker
	sessionID  string
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	serverAddr M.Socksaddr

	// access guards the upload state below; cond is signalled on every change
	// of pending / closed / uploadErr (Write, uploadLoop, Close, abort).
	access sync.Mutex
	cond   *sync.Cond
	// pending is the batch buffer: bytes written but not yet taken by
	// uploadLoop. Never longer than maxPost — Write blocks on a full buffer.
	pending []byte
	// maxPost is this stream's sc_max_each_post_bytes draw (one per conn, as
	// in Xray): the batch bound and the split size of a large write.
	maxPost int
	// uploadErr is sticky: the first failed POST, a failed download raise, or
	// the dead conn context. Every later Write (and a Read woken by the
	// teardown) returns it.
	uploadErr error
	seq       uint64
	lastPost  time.Time
	closed    bool
	closeOnce sync.Once
	// inflight is the semaphore of POSTs whose response is outstanding.
	inflight chan struct{}
	// loopDone closes when uploadLoop has exited; posts counts the POST
	// goroutines. Close and fail wait on both before releasing the pooled
	// connection, so a release never races a request on it.
	loopDone chan struct{}
	posts    sync.WaitGroup

	// lx: 050 — a late-bound reader makes a read deadline mandatory: until the
	// response arrives there is no reader to close, so a stalled Read could only
	// be released here.
	readDeadline *readDeadline
	// cancel kills the conn-scoped request context (lx: SPEC 072): the pending
	// download RoundTrip and any in-flight upload POST die with the conn.
	cancel context.CancelFunc
}

func newPacketConn(ctx context.Context, client *Client, sessionID string, serverAddr M.Socksaddr, xmux *xmuxRelease) *packetConn {
	maxPost := client.meta.scMaxEachPostBytes.rand()
	if maxPost <= 0 {
		maxPost = 1000000
	}
	maxInflight := packetUpMaxInflight
	if client.httpVersion == httpVersion11 || maxInflight < 1 {
		// lx: SPEC 104 — HTTP/1.1 upload POSTs stay on one keep-alive
		// connection; net/http cannot pipeline, a second concurrent POST would
		// open a second connection. Batching still applies.
		maxInflight = 1
	}
	conn := &packetConn{
		ctx:        ctx,
		client:     client,
		xmux:       xmux,
		breaker:    connBreaker{xmux: xmux},
		sessionID:  sessionID,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
		maxPost:    maxPost,
		inflight:   make(chan struct{}, maxInflight),
		loopDone:   make(chan struct{}),
	}
	conn.cond = sync.NewCond(&conn.access)
	conn.readDeadline = newReadDeadline(func() {
		conn.breaker.localClosed.Store(true) // lx: SPEC 076 — our teardown, not a remote failure
		select {
		case <-conn.created:
			if conn.reader != nil {
				conn.reader.Close()
			}
		default:
		}
	})
	// A conn context that dies from above (transport closed) must wake a Write
	// or the loop parked on cond; Close and abort broadcast themselves.
	context.AfterFunc(ctx, conn.cond.Broadcast)
	go conn.uploadLoop()
	return conn
}

// setupReader binds the download body (or the error that replaces it) and
// releases readers blocked in Read. Mirrors streamConn.setupReader.
func (c *packetConn) setupReader(reader io.ReadCloser, err error) {
	c.reader = reader
	c.readerErr = hideTransportError(err) // lx: SPEC 082, 104
	close(c.created)
}

// fail marks the download raise failed; see streamConn.fail (lx: SPEC 072).
// The upload side learns it through uploadErr (a Write after a dead raise
// returns the cause, not a timeout), cancelling the conn context fails the
// in-flight posts instantly, and the pooled connection is released once the
// loop and the posts are gone.
func (c *packetConn) fail(err error) {
	c.setupReader(nil, err)
	c.access.Lock()
	if c.uploadErr == nil {
		c.uploadErr = err
	}
	c.access.Unlock()
	c.cond.Broadcast()
	if c.cancel != nil {
		c.cancel()
	}
	c.releaseWhenIdle()
}

// releaseWhenIdle returns the pooled connection once no request of this conn
// can still be on it: the upload loop has exited and every POST goroutine has
// returned (the conn context is cancelled by the callers, so they return
// promptly). Idempotent through xmuxRelease.
func (c *packetConn) releaseWhenIdle() {
	<-c.loopDone
	c.posts.Wait()
	c.xmux.release()
}

func (c *packetConn) Read(b []byte) (int, error) {
	// Synchronise on created before touching reader/readerErr — the RoundTrip
	// goroutine writes them before close(created), which is the happens-before
	// edge (same contract as streamConn.Read).
	select {
	case <-c.created:
	case <-c.readDeadline.dead:
		return 0, os.ErrDeadlineExceeded
	}
	if c.readerErr != nil {
		return 0, c.uploadCause(c.readerErr)
	}
	n, err := c.reader.Read(b)
	c.breaker.noteRead(err) // lx: SPEC 076
	if err != nil && err != io.EOF {
		return n, c.uploadCause(c.breaker.outboundErr(hideTransportError(err), c.readDeadline)) // lx: SPEC 082, 094, 104 — after noteRead: the breaker classifies the raw error
	}
	return n, err
}

// uploadCause is the error a Read reports when the download died: the failed
// POST when an upload failure tore the stream down (abort cancelled the conn
// context, so the download side reports a cancellation that is only the
// consequence), otherwise the read error itself. After our own Close the read
// error stands.
func (c *packetConn) uploadCause(readErr error) error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.uploadErr != nil && !c.closed {
		return c.uploadErr
	}
	return readErr
}

// Write appends to the batch buffer and returns; uploadLoop turns the buffer
// into sequenced POSTs. A write larger than the buffer is delivered in
// maxPost-sized pieces as the loop drains it; a full buffer blocks the caller
// until the loop takes it (back-pressure), a dead conn fails it.
func (c *packetConn) Write(b []byte) (int, error) {
	c.access.Lock()
	defer c.access.Unlock()
	written := 0
	for written < len(b) {
		if c.closed {
			return written, net.ErrClosed
		}
		if c.uploadErr != nil {
			return written, c.uploadErr
		}
		if err := c.ctx.Err(); err != nil {
			return written, err
		}
		room := c.maxPost - len(c.pending)
		if room <= 0 {
			c.cond.Wait()
			continue
		}
		n := min(room, len(b)-written)
		c.pending = append(c.pending, b[written:written+n]...)
		written += n
		c.cond.Broadcast()
	}
	return written, nil
}

// uploadLoop drains the batch buffer into sequenced POSTs. Each post is
// issued in its own goroutine; the loop moves on to the next batch once the
// request body has been written (httptrace.WroteRequest — the order on the
// wire is the seq order) or the post ended, whichever first, and holds at
// packetUpMaxInflight outstanding responses. On HTTP/1.1 (one slot) it waits
// for the response, as before.
func (c *packetConn) uploadLoop() {
	defer close(c.loopDone)
	for {
		c.access.Lock()
		for len(c.pending) == 0 && !c.closed && c.uploadErr == nil && c.ctx.Err() == nil {
			c.cond.Wait()
		}
		if c.closed || c.uploadErr != nil || c.ctx.Err() != nil {
			c.access.Unlock()
			return
		}
		payload := c.pending
		c.pending = nil
		seq := c.seq
		c.seq++
		// Throttle: enforce the minimum inter-post interval since the last post.
		wait := c.nextPostDelay()
		c.access.Unlock()
		c.cond.Broadcast() // the buffer has room again

		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				c.abort(c.ctx.Err())
				return
			case <-timer.C:
			}
		}
		select {
		case c.inflight <- struct{}{}:
		case <-c.ctx.Done():
			c.abort(c.ctx.Err())
			return
		}
		wrote := make(chan struct{})
		done := make(chan struct{})
		c.posts.Add(1)
		go c.post(seq, payload, wrote, done)
		if cap(c.inflight) == 1 {
			wrote = nil // HTTP/1.1: the next post waits for this response
		}
		select {
		case <-wrote:
		case <-done:
		case <-c.ctx.Done():
			c.abort(c.ctx.Err())
			return
		}
	}
}

// post sends one sequenced upload POST and validates its response. wrote is
// closed when the request body has been written; done when the post ended.
func (c *packetConn) post(seq uint64, payload []byte, wrote, done chan struct{}) {
	defer c.posts.Done()
	defer close(done)
	defer func() { <-c.inflight }()

	// lx: SPEC 072 — one post is one bounded HTTP exchange: posts ride the
	// conn-scoped context (they must not die with the dial context), so the
	// bound has to be their own. Without it a wedged pooled connection blocks
	// the loop — and, through the full batch buffer, the WG send path behind
	// it — indefinitely. The bound grows with the payload (packetUpMinRate).
	timeout := packetUpPostTimeout + time.Duration(len(payload))*time.Second/time.Duration(packetUpMinRate)
	postCtx, postCancel := context.WithTimeout(c.ctx, timeout)
	defer postCancel()
	var wroteOnce sync.Once
	postCtx = httptrace.WithClientTrace(postCtx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			wroteOnce.Do(func() { close(wrote) })
		},
	})
	request, err := c.client.newRequest(postCtx, c.client.meta.uplinkHTTPMethod, c.sessionID, strconv.FormatUint(seq, 10), nil)
	if err != nil {
		c.abort(err)
		return
	}
	c.client.applyUplinkData(request, payload)

	// lx: 059 — every upload POST rides this stream's pooled connection and counts
	// against its h_max_request_times. packet-up issues one request per batch, so
	// counting streams instead of requests here would let the connection outlive
	// the limit the server was told about.
	response, err := c.xmux.roundTrip(request)
	if err != nil {
		c.abort(err)
		return
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		c.abort(E.New("v2ray-xhttp: unexpected upload status: ", response.Status))
		return
	}
	drainAndClose(response.Body)
	// lx: SPEC 076 — a drained 200 upload POST is genuine data flow: reset the
	// pooled connection's failure streak and the manager backoff.
	c.xmux.noteSuccess()
}

// abort records the first upload failure and tears the stream down: a lost
// packet leaves a hole in the server's reorder queue, nothing after it is
// delivered, so the consumer must learn now — through Write (uploadErr) and
// through Read (the download is closed under the local-teardown gate, so the
// breaker counts the one failure roundTrip already noted, not a second one
// for the body we closed). Neutral after our own Close.
func (c *packetConn) abort(err error) {
	c.access.Lock()
	if c.closed || c.uploadErr != nil {
		c.access.Unlock()
		c.cond.Broadcast()
		return
	}
	c.uploadErr = err
	c.access.Unlock()
	c.cond.Broadcast()
	c.breaker.localClosed.Store(true)
	select {
	case <-c.created:
		if c.reader != nil {
			c.reader.Close()
		}
	default:
	}
	if c.cancel != nil {
		c.cancel()
	}
}

// nextPostDelay returns how long to wait before the next post to honor
// sc_min_posts_interval_ms, updating lastPost to the projected post time. Caller
// must hold c.access.
func (c *packetConn) nextPostDelay() time.Duration {
	interval := time.Duration(c.client.meta.scMinPostsIntervalMs.rand()) * time.Millisecond
	now := timeNow()
	if c.lastPost.IsZero() || interval <= 0 {
		c.lastPost = now
		return 0
	}
	earliest := c.lastPost.Add(interval)
	if earliest.After(now) {
		c.lastPost = earliest
		return earliest.Sub(now)
	}
	c.lastPost = now
	return 0
}

func (c *packetConn) Close() error {
	c.access.Lock()
	c.closed = true
	c.access.Unlock()
	c.cond.Broadcast()
	// The reader is late-bound: a conn closed before the download response
	// arrived has nothing to close — the conn-context cancel below aborts the
	// pending RoundTrip (lx: SPEC 072; it used to lean on the dial context,
	// which an unbounded dial context never fires). Guard with closeOnce so a
	// second Close cannot double-close the body.
	var err error
	c.closeOnce.Do(func() {
		c.breaker.localClosed.Store(true) // lx: SPEC 076 — before closing the reader
		c.readDeadline.stop()
		select {
		case <-c.created:
			if c.reader != nil {
				err = c.reader.Close()
			}
		default:
		}
		if c.cancel != nil {
			c.cancel()
		}
		// lx: 059 — release the pooled connection last, once nothing reads it and
		// no further upload POST can be issued (the loop has exited, the posts
		// are cancelled and gone).
		c.releaseWhenIdle()
	})
	return err
}

func (c *packetConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *packetConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — read deadlines are real now that the reader is late-bound (an
// unbound reader could otherwise stall Read forever). Writes land in the batch
// buffer; the posts behind it are individual HTTP requests each bounded by
// packetUpPostTimeout (lx: SPEC 072), so a write deadline has nothing to arm
// against and stays unsupported, as before.
func (c *packetConn) SetDeadline(t time.Time) error      { return c.readDeadline.set(t) }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }

// NeedAdditionalReadDeadline: the read deadline is one-shot, as in streamConn.
func (c *packetConn) NeedAdditionalReadDeadline() bool { return true }
