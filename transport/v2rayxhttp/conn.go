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

	conn := newStreamConn(pipeWriter, c.serverAddr)
	go func() {
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
	conn := newSplitConn(downResp.Body, pipeWriter, c.serverAddr)
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
}

func newStreamConn(writer *io.PipeWriter, serverAddr M.Socksaddr) *streamConn {
	return &streamConn{
		writer:     writer,
		created:    make(chan struct{}),
		serverAddr: serverAddr,
	}
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
	<-c.created
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

func (c *streamConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *streamConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *streamConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *streamConn) NeedAdditionalReadDeadline() bool   { return true }

// splitConn pairs an already-open download reader with an upload pipe (stream-up
// mode). The download body is ready immediately; the upload POST is driven by
// the caller in a goroutine.
type splitConn struct {
	reader     io.ReadCloser
	writer     *io.PipeWriter
	serverAddr M.Socksaddr
	closeOnce  sync.Once
}

func newSplitConn(reader io.ReadCloser, writer *io.PipeWriter, serverAddr M.Socksaddr) *splitConn {
	return &splitConn{
		reader:     reader,
		writer:     writer,
		serverAddr: serverAddr,
	}
}

func (c *splitConn) uploadFailed(err error) {
	c.writer.CloseWithError(err)
}

func (c *splitConn) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c *splitConn) Write(b []byte) (int, error) { return c.writer.Write(b) }

func (c *splitConn) Close() error {
	c.closeOnce.Do(func() {
		c.writer.Close()
		c.reader.Close()
	})
	return nil
}

func (c *splitConn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *splitConn) RemoteAddr() net.Addr               { return c.serverAddr }
func (c *splitConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *splitConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *splitConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *splitConn) NeedAdditionalReadDeadline() bool   { return true }

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
