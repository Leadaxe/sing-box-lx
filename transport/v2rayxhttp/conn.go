package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// packetUpPostTimeout bounds one packet-up upload POST (lx: SPEC 072). Posts
// ride the conn-scoped context (see dialPacketUp), not the dial context, so
// without an own bound a wedged pooled connection would block a Write — and
// the WG send path behind it — forever. One post is one HTTP exchange; the
// probe budget C.TCPTimeout is the same ceiling the WG bind dial uses.
// Variable, not const: tests shrink it.
var packetUpPostTimeout = C.TCPTimeout

// transportContext is the transport-lifetime context conn-scoped request
// contexts derive from (lx: SPEC 072). NewClient always sets c.ctx; the
// Background fallback keeps literal-constructed clients (tests) valid.
func (c *Client) transportContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// applyGRPCHeader sets the streamed-body Content-Type that Xray sends on
// stream-one/stream-up requests (FillStreamRequest in
// transport/internet/splithttp/config.go). Reverse proxies and CDNs in front of
// an XHTTP server key response streaming on a gRPC content type; without it the
// download side is buffered and the dial hangs until timeout. Opt out with
// no_grpc_header, matching Xray's NoGRPCHeader.
func (c *Client) applyGRPCHeader(request *http.Request) {
	if c.noGRPCHeader || request.Body == nil {
		return
	}
	request.Header.Set("Content-Type", "application/grpc")
}

// dialStreamOne opens a single bidirectional HTTP/2 stream: the request body is
// the upload direction, the response body is the download direction. With Reality
// it is also what "auto" resolves to (matching Xray).
//
// Unlike stream-up/packet-up, the request targets the BARE path with NO sessionId:
// Xray's splithttp server keys the stream-one (bidirectional) branch on an empty
// sessionId. Sending "<path>/<sessionId>" instead routes the server into the
// stream-down branch, which never pairs with a stream-up POST, so the response
// body carries non-VLESS bytes and the VLESS layer fails with "unknown version".
func (c *Client) dialStreamOne(ctx context.Context, sessionID string, xmuxClient *xmuxClient) (net.Conn, error) {
	_ = sessionID // intentionally unused: stream-one sends no sessionId on the wire
	pipeReader, pipeWriter := io.Pipe()
	// lx: SPEC 072 — the request rides a conn-scoped context under the TRANSPORT
	// lifetime, not the dial context. http2 binds the whole stream to the request
	// context, and a dial context is allowed to carry a deadline that outlives
	// the dial (the WG bind dials under C.TCPTimeout since SPEC 071): on the dial
	// context the deadline would abort the raised stream when it fires, cycling
	// every healthy detour conn at 15 s. The dial context bounds the raise only,
	// through the 050 guard below; conn teardown cancels connCtx via Close/fail.
	connCtx, connCancel := context.WithCancel(c.transportContext())
	// stream-one carries a body, so it uses the configured upload method (default
	// POST); the empty sessionID keeps the request on the bare path.
	request, err := c.newRequest(connCtx, c.meta.uplinkHTTPMethod, "", "", pipeReader)
	if err != nil {
		connCancel()
		return nil, err
	}
	c.applyGRPCHeader(request)

	conn := newStreamConn(pipeReader, pipeWriter, c.serverAddr, newXmuxRelease(xmuxClient))
	conn.cancel = connCancel
	// lx: 050 — the conn is handed up before RoundTrip has raised the stream, so
	// anything written meanwhile (the VLESS/encryption handshake) blocks on an
	// unread pipe. Until the stream is up, cancelling the dial context must free
	// that write; the guard stops at `created` so it can never tear down a live
	// connection once the stream exists.
	stopGuard := watchDialContext(ctx, conn.created, func(err error) {
		// Break the pipe from the read half so the blocked Write sees this error
		// rather than a bare ErrClosedPipe (see writeDeadline).
		pipeReader.CloseWithError(err)
		// lx: SPEC 072 — with the request on connCtx the dial context no longer
		// aborts the pending RoundTrip by itself; do it here.
		connCancel()
	})
	go func() {
		defer stopGuard()
		response, err := xmuxClient.roundTrip(request)
		if err != nil {
			conn.fail(err)
			return
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			conn.fail(E.New("v2ray-xhttp: unexpected status: ", response.Status))
			return
		}
		conn.setupReader(response.Body, nil)
	}()
	return conn, nil
}

// watchDialContext releases a pending write on the upload pipe if the dial
// context is cancelled before the stream is up. It returns a stop function; the
// watcher also exits on `done`, so a connection that reached the live stage is
// never affected by later cancellation of its dial context (SPECS/TASKS/050).
func watchDialContext(ctx context.Context, done <-chan struct{}, onCancel func(error)) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			onCancel(ctx.Err())
		case <-done:
		case <-stop:
		}
	}()
	return func() { close(stop) }
}

// dialStreamUp opens a streamed POST for the upload direction and a separate GET
// whose response body is the download direction.
//
// Like packet-up, the download response is awaited asynchronously: an Xray
// server holds the stream-down response until the paired stream-up request has
// delivered bytes, so blocking on it here deadlocks the dial and the fronting
// proxy answers 504. See the note on dialPacketUp. lx: SPEC 002.
func (c *Client) dialStreamUp(ctx context.Context, sessionID string, xmuxClient *xmuxClient) (net.Conn, error) {
	// lx: SPEC 072 — conn-scoped request context; see dialStreamOne.
	connCtx, connCancel := context.WithCancel(c.transportContext())
	// Download: GET response body (no seq — stream mode).
	downReq, err := c.newRequest(connCtx, http.MethodGet, sessionID, "", nil)
	if err != nil {
		connCancel()
		return nil, err
	}

	// Upload: streamed body request using the configured upload method.
	pipeReader, pipeWriter := io.Pipe()
	upReq, err := c.newRequest(connCtx, c.meta.uplinkHTTPMethod, sessionID, "", pipeReader)
	if err != nil {
		connCancel()
		return nil, err
	}
	c.applyGRPCHeader(upReq)
	conn := newSplitConn(pipeReader, pipeWriter, c.serverAddr, newXmuxRelease(xmuxClient))
	conn.cancel = connCancel
	// lx: SPEC 072 — stream-up used to inherit dial-context teardown through its
	// requests; with requests on connCtx it needs the same raise guard as
	// stream-one (SPEC 050 mechanism, stands down once the download is up).
	stopGuard := watchDialContext(ctx, conn.created, func(err error) {
		pipeReader.CloseWithError(err)
		connCancel()
	})
	go func() {
		defer stopGuard()
		downResp, err := xmuxClient.roundTrip(downReq)
		if err != nil {
			conn.fail(E.Cause(err, "open download"))
			return
		}
		if downResp.StatusCode != http.StatusOK {
			downResp.Body.Close()
			conn.fail(E.New("v2ray-xhttp: unexpected download status: ", downResp.Status))
			return
		}
		conn.setupReader(downResp.Body, nil)
	}()
	go func() {
		upResp, err := xmuxClient.roundTrip(upReq)
		if err != nil {
			conn.uploadFailed(err)
			return
		}
		drainAndClose(upResp.Body)
	}()
	return conn, nil
}

// dialPacketUp opens a GET download stream and sends uploads as sequential POST
// packets, one HTTP request per Write.
//
// The download RoundTrip runs in a goroutine and the conn is handed up
// immediately, WITHOUT waiting for the response headers. Waiting for them
// deadlocks against an Xray server: it only has downlink bytes to send once the
// session has received an uplink packet, so it withholds the response until the
// first upload arrives — while we withheld the first upload until the response
// arrived. Neither side moves and the reverse proxy in front (nginx/CDN) kills
// the request, surfacing as "504 Gateway Timeout" after its upstream timeout.
// Wire-reproduced against a VK-CDN → nginx → Xray path, where the reference
// client (sing-box-extended) works: its OpenStream likewise returns as soon as
// the connection is established (httptrace GotConn) and processes the response
// asynchronously. lx: SPEC 002.
func (c *Client) dialPacketUp(ctx context.Context, sessionID string, xmuxClient *xmuxClient) (net.Conn, error) {
	// lx: SPEC 072 — conn-scoped request context (see dialStreamOne); the
	// download GET and every upload POST ride it, and Close/fail cancel it. The
	// dial context is deliberately NOT watched here: the download response is
	// allowed to arrive only after the first upload (see the deadlock note
	// above), so it is no raise precondition; a caller that cancels its dial
	// abandons the conn and tears everything down via Close.
	_ = ctx
	connCtx, connCancel := context.WithCancel(c.transportContext())
	// Download stream: GET with the session id but no seq (downlink).
	downReq, err := c.newRequest(connCtx, http.MethodGet, sessionID, "", nil)
	if err != nil {
		connCancel()
		return nil, err
	}
	conn := newPacketConn(connCtx, c, sessionID, c.serverAddr, newXmuxRelease(xmuxClient))
	conn.cancel = connCancel
	go func() {
		downResp, err := xmuxClient.roundTrip(downReq)
		if err != nil {
			conn.fail(E.Cause(err, "open download"))
			return
		}
		if downResp.StatusCode != http.StatusOK {
			downResp.Body.Close()
			conn.fail(E.New("v2ray-xhttp: unexpected download status: ", downResp.Status))
			return
		}
		conn.setupReader(downResp.Body, nil)
	}()
	return conn, nil
}

// lx:begin 050 deadline-support (SPECS/TASKS/050)
//
// A streamed body is an io.Pipe: a Write blocks until RoundTrip starts reading
// it, which on a half-alive node (TCP accepted, stream never raised) is never.
// io.Pipe has no deadlines of its own, but CloseWithError releases a pending
// Write instantly — so a deadline is a timer that closes the pipe with
// os.ErrDeadlineExceeded. Without this the blocked goroutine is unkillable and
// outlives box shutdown; see the task for the field evidence.
//
// The read side is a late-bound response body, so it cannot be closed before it
// exists. Read therefore waits on `dead` alongside `created`, and an expired
// read deadline closes `dead` rather than touching the reader.
// The pipe is broken from the READ half on purpose: io.Pipe hands a writer only
// ErrClosedPipe for an error set via PipeWriter.CloseWithError (writeCloseError
// prefers rerr, and werr suppresses it), so closing the write half would lose
// os.ErrDeadlineExceeded. Closing the read half sets rerr, which the blocked
// Write does surface.
type writeDeadline struct {
	access sync.Mutex
	reader *io.PipeReader
	timer  *time.Timer
}

// set arms (or re-arms) the write deadline. A zero time clears it; a time in the
// past fires immediately, matching net.Conn semantics.
func (d *writeDeadline) set(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() || d.reader == nil {
		return nil
	}
	if delay := time.Until(t); delay <= 0 {
		d.reader.CloseWithError(os.ErrDeadlineExceeded)
	} else {
		d.timer = time.AfterFunc(delay, func() {
			d.reader.CloseWithError(os.ErrDeadlineExceeded)
		})
	}
	return nil
}

// stop releases the timer; called from Close so an armed deadline cannot outlive
// the conn.
func (d *writeDeadline) stop() {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// readDeadline unblocks a pending Read by closing `dead`. Read observes it both
// while waiting for the late-bound reader and while blocked in reader.Read —
// the latter needs the reader closed too, which the owner does via onExpire.
type readDeadline struct {
	access   sync.Mutex
	dead     chan struct{}
	expired  bool
	timer    *time.Timer
	onExpire func()
}

func newReadDeadline(onExpire func()) *readDeadline {
	return &readDeadline{dead: make(chan struct{}), onExpire: onExpire}
}

func (d *readDeadline) set(t time.Time) error {
	d.access.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() || d.expired {
		d.access.Unlock()
		return nil
	}
	var fire bool
	if delay := time.Until(t); delay <= 0 {
		fire = d.expireLocked()
	} else {
		d.timer = time.AfterFunc(delay, d.expire)
	}
	d.access.Unlock()
	// lx: SPEC 074 — onExpire runs OUTSIDE the lock. It closes the HTTP/2 response
	// body, which can block (h2 waits on its own machinery); holding `access`
	// across that call deadlocks every other user of this deadline — including
	// quic-go, which calls SetReadDeadline from Transport.Close on the very path
	// that is trying to expire. Observed live: a QUIC handshake over an xhttp hop
	// hung ~20-90 s with three goroutines stacked on this mutex.
	if fire {
		d.runOnExpire()
	}
	return nil
}

func (d *readDeadline) expire() {
	d.access.Lock()
	fire := d.expireLocked()
	d.access.Unlock()
	if fire {
		d.runOnExpire()
	}
}

// expireLocked marks the deadline expired and reports whether the caller now owns
// running onExpire (exactly once). The callback itself must run unlocked.
func (d *readDeadline) expireLocked() bool {
	if d.expired {
		return false
	}
	d.expired = true
	close(d.dead)
	return d.onExpire != nil
}

func (d *readDeadline) runOnExpire() {
	if d.onExpire != nil {
		d.onExpire()
	}
}

func (d *readDeadline) stop() {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// lx:end 050 deadline-support

// streamConn is a net.Conn whose write side is the upload pipe and whose read
// side is a late-bound response body (download). It mirrors the late-binding
// pattern of v2rayhttp.HTTP2Conn but is self-contained here.
type streamConn struct {
	// xmux releases this stream's pooled connection when the conn closes.
	xmux       *xmuxRelease
	writer     *io.PipeWriter
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// lx: 050 — deadlines; without them a blocked Write/Read is unkillable.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
	// cancel kills the conn-scoped request context (lx: SPEC 072); set by the
	// dial, invoked by Close and fail.
	cancel context.CancelFunc
}

func newStreamConn(reader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr, xmux *xmuxRelease) *streamConn {
	conn := &streamConn{
		xmux:       xmux,
		writer:     writer,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
	}
	conn.writeDeadline.reader = reader
	// lx: 050 — an expired read deadline must also close an already-bound reader,
	// otherwise Read stays blocked inside reader.Read.
	conn.readDeadline = newReadDeadline(func() {
		select {
		case <-conn.created:
			if conn.reader != nil {
				conn.reader.Close()
			}
		default:
		}
	})
	return conn
}

func (c *streamConn) setupReader(reader io.ReadCloser, err error) {
	c.reader = reader
	c.readerErr = err
	close(c.created)
}

// fail marks the raise failed (lx: SPEC 072): it binds the error for readers
// AND breaks the upload pipe from the read half, so a Write blocked on (or
// arriving at) a pipe nobody will ever read surfaces the failure instead of
// hanging forever. Closing `created` alone is not enough: the 050 guard stands
// down on it, and the field dump behind SPEC 072 is a VLESS handshake Write
// that outlived its dial context by 38 minutes exactly that way. The conn
// context and the pooled connection are released here too, because the
// error path of a dial has no guaranteed Close: sing-vmess early dials return
// the conn together with the write error and callers drop it.
func (c *streamConn) fail(err error) {
	c.setupReader(nil, err)
	c.writeDeadline.reader.CloseWithError(err)
	if c.cancel != nil {
		c.cancel()
	}
	c.xmux.release()
}

func (c *streamConn) Read(b []byte) (int, error) {
	// Always synchronise on created before touching reader/readerErr: the RoundTrip
	// goroutine writes them before close(created), so the receive is the happens-
	// before edge. The old `if c.reader == nil` fast path read reader unsynchronised
	// (a data race, -race flagged it) for no gain — a receive on an already-closed
	// channel is effectively free (SPEC 022 #7).
	//
	// lx: 050 — also wait on the read deadline: until RoundTrip binds the reader
	// there is nothing to close, so an expired deadline can only be observed here.
	select {
	case <-c.created:
	case <-c.readDeadline.dead:
		return 0, os.ErrDeadlineExceeded
	}
	if c.readerErr != nil {
		return 0, c.readerErr
	}
	return c.reader.Read(b)
}

func (c *streamConn) Write(b []byte) (int, error) {
	return c.writer.Write(b)
}

func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		// lx: 050 — drop armed timers first so neither can fire on a closed conn.
		c.writeDeadline.stop()
		c.readDeadline.stop()
		c.writer.Close()
		select {
		case <-c.created:
			if c.reader != nil {
				c.reader.Close()
			}
		default:
		}
		// lx: SPEC 072 — kill the conn-scoped request context so a pending or
		// live RoundTrip cannot outlive the conn.
		if c.cancel != nil {
			c.cancel()
		}
		// lx: 059 — release the pooled connection last, once nothing reads it.
		c.xmux.release()
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *streamConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — real deadlines (were os.ErrInvalid): a Write into an unread upload
// pipe is otherwise unkillable and survives box shutdown.
func (c *streamConn) SetDeadline(t time.Time) error {
	if err := c.readDeadline.set(t); err != nil {
		return err
	}
	return c.writeDeadline.set(t)
}

func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.writeDeadline.set(t) }

// NeedAdditionalReadDeadline stays true even though SetReadDeadline now works:
// the read deadline here is one-shot (it closes the late-bound body to break a
// pending Read) and does not restore the conn for a later read, which is what
// net.Conn semantics and deadline.NewConn provide. The escape this task needs is
// on the write side, so keep the wrapper rather than claim semantics we lack.
func (c *streamConn) NeedAdditionalReadDeadline() bool { return true }

// splitConn pairs an already-open download reader with an upload pipe (stream-up
// mode). The download body is ready immediately; the upload POST is driven by
// the caller in a goroutine.
type splitConn struct {
	// xmux releases this stream's pooled connection when the conn closes.
	xmux       *xmuxRelease
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	writer     *io.PipeWriter
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// lx: 050 — same unkillable-Write exposure as streamConn. The reader is
	// late-bound (see dialStreamUp), so an expired read deadline must handle
	// both the bound and the not-yet-bound case.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
	// cancel kills the conn-scoped request context (lx: SPEC 072).
	cancel context.CancelFunc
}

func newSplitConn(uploadReader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr, xmux *xmuxRelease) *splitConn {
	conn := &splitConn{
		created:    make(chan struct{}),
		writer:     writer,
		serverAddr: serverAddr,
	}
	conn.writeDeadline.reader = uploadReader
	conn.readDeadline = newReadDeadline(func() {
		select {
		case <-conn.created:
			if conn.reader != nil {
				conn.reader.Close()
			}
		default:
		}
	})
	return conn
}

// setupReader binds the download body (or its error) and releases blocked Reads.
func (c *splitConn) setupReader(reader io.ReadCloser, err error) {
	c.reader = reader
	c.readerErr = err
	close(c.created)
}

// fail marks the raise failed; see streamConn.fail (lx: SPEC 072). A dead
// download side kills the conn as a whole — VLESS can never read a response —
// so the upload pipe is broken too, freeing any writer parked on it.
func (c *splitConn) fail(err error) {
	c.setupReader(nil, err)
	c.writeDeadline.reader.CloseWithError(err)
	if c.cancel != nil {
		c.cancel()
	}
	c.xmux.release()
}

// uploadFailed breaks the upload pipe from the READ half, so the blocked
// writer surfaces the actual upload error — a write-half CloseWithError hands
// the writer a bare ErrClosedPipe (io.Pipe's writeCloseError prefers rerr and
// suppresses werr; see the writeDeadline note). lx: SPEC 072.
func (c *splitConn) uploadFailed(err error) {
	c.writeDeadline.reader.CloseWithError(err)
}

func (c *splitConn) Read(b []byte) (int, error) {
	// Late-bound reader: synchronise on created, exactly as streamConn.Read does.
	select {
	case <-c.created:
	case <-c.readDeadline.dead:
		return 0, os.ErrDeadlineExceeded
	}
	if c.readerErr != nil {
		return 0, c.readerErr
	}
	return c.reader.Read(b)
}
func (c *splitConn) Write(b []byte) (int, error) { return c.writer.Write(b) }

func (c *splitConn) Close() error {
	c.closeOnce.Do(func() {
		// lx: 050 — drop armed timers before closing, as in streamConn.
		c.writeDeadline.stop()
		c.readDeadline.stop()
		c.writer.Close()
		// The reader may not be bound yet (see dialStreamUp); a pending
		// RoundTrip is torn down by the conn-context cancel below.
		select {
		case <-c.created:
			if c.reader != nil {
				c.reader.Close()
			}
		default:
		}
		// lx: SPEC 072 — kill the conn-scoped request context (aborts pending
		// download/upload RoundTrips).
		if c.cancel != nil {
			c.cancel()
		}
		// lx: 059 — release the pooled connection last, once nothing reads it.
		c.xmux.release()
	})
	return nil
}

func (c *splitConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *splitConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — real deadlines (were os.ErrInvalid); see streamConn.
func (c *splitConn) SetDeadline(t time.Time) error {
	if err := c.readDeadline.set(t); err != nil {
		return err
	}
	return c.writeDeadline.set(t)
}

func (c *splitConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *splitConn) SetWriteDeadline(t time.Time) error { return c.writeDeadline.set(t) }

// NeedAdditionalReadDeadline: see streamConn — the read deadline is one-shot.
func (c *splitConn) NeedAdditionalReadDeadline() bool { return true }

// packetConn implements packet-up: download is a GET response body, each Write
// is delivered as a sequential POST to "<path>/<sessionId>/<seq>".
//
// The reader is late-bound: dialPacketUp hands the conn up before the download
// response exists (see the deadlock note there), so Read waits on `created`
// until the RoundTrip goroutine binds either a body or an error.
type packetConn struct {
	ctx    context.Context
	client *Client
	// xmux is the pooled connection carrying every upload POST of this stream;
	// released when the conn closes.
	xmux       *xmuxRelease
	sessionID  string
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	serverAddr M.Socksaddr
	access     sync.Mutex
	seq        uint64
	lastPost   time.Time
	closed     bool
	closeOnce  sync.Once
	// lx: 050 — a late-bound reader makes a read deadline mandatory: until the
	// response arrives there is no reader to close, so a stalled Read could only
	// be released here.
	readDeadline *readDeadline
	// cancel kills the conn-scoped request context (lx: SPEC 072): the pending
	// download RoundTrip and any in-flight upload POST die with the conn.
	cancel context.CancelFunc
}

func newPacketConn(ctx context.Context, client *Client, sessionID string, serverAddr M.Socksaddr, xmux *xmuxRelease) *packetConn {
	conn := &packetConn{
		ctx:        ctx,
		client:     client,
		xmux:       xmux,
		sessionID:  sessionID,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
	}
	conn.readDeadline = newReadDeadline(func() {
		select {
		case <-conn.created:
			if conn.reader != nil {
				conn.reader.Close()
			}
		default:
		}
	})
	return conn
}

// setupReader binds the download body (or the error that replaces it) and
// releases readers blocked in Read. Mirrors streamConn.setupReader.
func (c *packetConn) setupReader(reader io.ReadCloser, err error) {
	c.reader = reader
	c.readerErr = err
	close(c.created)
}

// fail marks the download raise failed; see streamConn.fail (lx: SPEC 072).
// There is no upload pipe to break — posts are individual bounded requests —
// but cancelling the conn context fails them instantly, so a dead session
// cannot keep posting into the void, and the pooled connection is released
// for the dropped-conn error path.
func (c *packetConn) fail(err error) {
	c.setupReader(nil, err)
	if c.cancel != nil {
		c.cancel()
	}
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
		return 0, c.readerErr
	}
	return c.reader.Read(b)
}

// Write delivers a write as one or more sequential upload POSTs. A write larger
// than sc_max_each_post_bytes is split into multiple sequenced packets; successive
// posts are throttled by sc_min_posts_interval_ms (anti-burst). Each packet carries
// the payload per uplink_data_placement.
func (c *packetConn) Write(b []byte) (int, error) {
	maxEach := c.client.meta.scMaxEachPostBytes.rand()
	if maxEach <= 0 {
		maxEach = len(b)
	}
	written := 0
	for written < len(b) {
		end := written + maxEach
		if end > len(b) {
			end = len(b)
		}
		if err := c.sendPacket(b[written:end]); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

// sendPacket posts a single sequenced upload chunk.
func (c *packetConn) sendPacket(b []byte) error {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return net.ErrClosed
	}
	seq := c.seq
	c.seq++
	// Throttle: enforce the minimum inter-post interval since the last post.
	wait := c.nextPostDelay()
	c.access.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return c.ctx.Err()
		case <-timer.C:
		}
	}

	payload := make([]byte, len(b))
	copy(payload, b)
	// lx: SPEC 072 — one post is one bounded HTTP exchange: posts ride the
	// conn-scoped context (they must not die with the dial context), so the
	// bound has to be their own. Without it a wedged pooled connection blocks
	// this Write — and the WG send path behind it — indefinitely.
	postCtx, postCancel := context.WithTimeout(c.ctx, packetUpPostTimeout)
	defer postCancel()
	request, err := c.client.newRequest(postCtx, c.client.meta.uplinkHTTPMethod, c.sessionID, strconv.FormatUint(seq, 10), nil)
	if err != nil {
		return err
	}
	c.client.applyUplinkData(request, payload)

	// lx: 059 — every upload POST rides this stream's pooled connection and counts
	// against its h_max_request_times. packet-up issues one request per Write, so
	// counting streams instead of requests here would let the connection outlive
	// the limit the server was told about.
	response, err := c.xmux.roundTrip(request)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return E.New("v2ray-xhttp: unexpected upload status: ", response.Status)
	}
	drainAndClose(response.Body)
	return nil
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
	// The reader is late-bound: a conn closed before the download response
	// arrived has nothing to close — the conn-context cancel below aborts the
	// pending RoundTrip (lx: SPEC 072; it used to lean on the dial context,
	// which an unbounded dial context never fires). Guard with closeOnce so a
	// second Close cannot double-close the body.
	var err error
	c.closeOnce.Do(func() {
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
		// no further upload POST can be issued (c.closed is already set above).
		c.xmux.release()
	})
	return err
}

func (c *packetConn) LocalAddr() net.Addr  { return M.Socksaddr{} }
func (c *packetConn) RemoteAddr() net.Addr { return c.serverAddr }

// lx: 050 — read deadlines are real now that the reader is late-bound (an
// unbound reader could otherwise stall Read forever). Writes are individual
// HTTP requests each bounded by packetUpPostTimeout (lx: SPEC 072), so a write
// deadline has nothing to arm against and stays unsupported, as before.
func (c *packetConn) SetDeadline(t time.Time) error      { return c.readDeadline.set(t) }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return c.readDeadline.set(t) }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }

// NeedAdditionalReadDeadline: the read deadline is one-shot, as in streamConn.
func (c *packetConn) NeedAdditionalReadDeadline() bool { return true }

// byteReader is a one-shot reader over a byte slice used as a fixed-length
// request body for packet-up uploads.
type byteReader struct {
	data []byte
	off  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
