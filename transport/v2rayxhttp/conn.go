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

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

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
func (c *Client) dialStreamOne(ctx context.Context, sessionID string) (net.Conn, error) {
	_ = sessionID // intentionally unused: stream-one sends no sessionId on the wire
	pipeReader, pipeWriter := io.Pipe()
	// stream-one carries a body, so it uses the configured upload method (default
	// POST); the empty sessionID keeps the request on the bare path.
	request, err := c.newRequest(ctx, c.meta.uplinkHTTPMethod, "", "", pipeReader)
	if err != nil {
		return nil, err
	}
	c.applyGRPCHeader(request)

	conn := newStreamConn(pipeReader, pipeWriter, c.serverAddr)
	// lx: 050 — the conn is handed up before RoundTrip has raised the stream, so
	// anything written meanwhile (the VLESS/encryption handshake) blocks on an
	// unread pipe. Until the stream is up, cancelling the dial context must free
	// that write; the guard stops at `created` so it can never tear down a live
	// connection once the stream exists.
	stopGuard := watchDialContext(ctx, conn.created, func(err error) {
		// Break the pipe from the read half so the blocked Write sees this error
		// rather than a bare ErrClosedPipe (see writeDeadline).
		pipeReader.CloseWithError(err)
	})
	go func() {
		defer stopGuard()
		response, err := c.transport.RoundTrip(request)
		if err != nil {
			conn.setupReader(nil, err)
			return
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			conn.setupReader(nil, E.New("v2ray-xhttp: unexpected status: ", response.Status))
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
func (c *Client) dialStreamUp(ctx context.Context, sessionID string) (net.Conn, error) {
	// Download: GET response body (no seq — stream mode).
	downReq, err := c.newRequest(ctx, http.MethodGet, sessionID, "", nil)
	if err != nil {
		return nil, err
	}
	downResp, err := c.transport.RoundTrip(downReq)
	if err != nil {
		return nil, E.Cause(err, "open download")
	}
	if downResp.StatusCode != http.StatusOK {
		downResp.Body.Close()
		return nil, E.New("v2ray-xhttp: unexpected download status: ", downResp.Status)
	}

	// Upload: streamed body request using the configured upload method.
	pipeReader, pipeWriter := io.Pipe()
	upReq, err := c.newRequest(ctx, c.meta.uplinkHTTPMethod, sessionID, "", pipeReader)
	if err != nil {
		downResp.Body.Close()
		return nil, err
	}
	c.applyGRPCHeader(upReq)
	conn := newSplitConn(downResp.Body, pipeReader, pipeWriter, c.serverAddr)
	go func() {
		upResp, err := c.transport.RoundTrip(upReq)
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
func (c *Client) dialPacketUp(ctx context.Context, sessionID string) (net.Conn, error) {
	// Download stream: GET with the session id but no seq (downlink).
	downReq, err := c.newRequest(ctx, http.MethodGet, sessionID, "", nil)
	if err != nil {
		return nil, err
	}
	downResp, err := c.transport.RoundTrip(downReq)
	if err != nil {
		return nil, E.Cause(err, "open download")
	}
	if downResp.StatusCode != http.StatusOK {
		downResp.Body.Close()
		return nil, E.New("v2ray-xhttp: unexpected download status: ", downResp.Status)
	}
	return &packetConn{
		ctx:        ctx,
		client:     c,
		sessionID:  sessionID,
		reader:     downResp.Body,
		serverAddr: c.serverAddr,
	}, nil
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
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() || d.expired {
		return nil
	}
	if delay := time.Until(t); delay <= 0 {
		d.expireLocked()
	} else {
		d.timer = time.AfterFunc(delay, d.expire)
	}
	return nil
}

func (d *readDeadline) expire() {
	d.access.Lock()
	defer d.access.Unlock()
	d.expireLocked()
}

func (d *readDeadline) expireLocked() {
	if d.expired {
		return
	}
	d.expired = true
	close(d.dead)
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
	writer     *io.PipeWriter
	reader     io.ReadCloser
	created    chan struct{}
	readerErr  error
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// lx: 050 — deadlines; without them a blocked Write/Read is unkillable.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
}

func newStreamConn(reader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr) *streamConn {
	conn := &streamConn{
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
	reader     io.ReadCloser
	writer     *io.PipeWriter
	serverAddr M.Socksaddr
	closeOnce  sync.Once
	// lx: 050 — same unkillable-Write exposure as streamConn; the reader here is
	// bound up front, so an expired read deadline just closes it.
	writeDeadline writeDeadline
	readDeadline  *readDeadline
}

func newSplitConn(reader io.ReadCloser, uploadReader *io.PipeReader, writer *io.PipeWriter, serverAddr M.Socksaddr) *splitConn {
	conn := &splitConn{
		reader:     reader,
		writer:     writer,
		serverAddr: serverAddr,
	}
	conn.writeDeadline.reader = uploadReader
	conn.readDeadline = newReadDeadline(func() {
		if conn.reader != nil {
			conn.reader.Close()
		}
	})
	return conn
}

func (c *splitConn) uploadFailed(err error) {
	c.writer.CloseWithError(err)
}

func (c *splitConn) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c *splitConn) Write(b []byte) (int, error) { return c.writer.Write(b) }

func (c *splitConn) Close() error {
	c.closeOnce.Do(func() {
		// lx: 050 — drop armed timers before closing, as in streamConn.
		c.writeDeadline.stop()
		c.readDeadline.stop()
		c.writer.Close()
		c.reader.Close()
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
type packetConn struct {
	ctx        context.Context
	client     *Client
	sessionID  string
	reader     io.ReadCloser
	serverAddr M.Socksaddr
	access     sync.Mutex
	seq        uint64
	lastPost   time.Time
	closed     bool
}

func (c *packetConn) Read(b []byte) (int, error) {
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
	request, err := c.client.newRequest(c.ctx, c.client.meta.uplinkHTTPMethod, c.sessionID, strconv.FormatUint(seq, 10), nil)
	if err != nil {
		return err
	}
	c.client.applyUplinkData(request, payload)

	response, err := c.client.transport.RoundTrip(request)
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
	return c.reader.Close()
}

func (c *packetConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *packetConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *packetConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *packetConn) NeedAdditionalReadDeadline() bool   { return true }

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
