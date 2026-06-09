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

// dialStreamOne opens a single bidirectional HTTP/2 stream: the request body is
// the upload direction, the response body is the download direction. This is
// the simplest XHTTP mode and the fallback for "auto".
func (c *Client) dialStreamOne(ctx context.Context, sessionID string) (net.Conn, error) {
	u, err := c.requestURL(sessionID)
	if err != nil {
		return nil, err
	}
	pipeReader, pipeWriter := io.Pipe()
	request := c.newRequest(ctx, http.MethodPost, u, pipeReader)

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
	downURL, err := c.requestURL(sessionID)
	if err != nil {
		return nil, err
	}
	upURL, err := c.requestURL(sessionID)
	if err != nil {
		return nil, err
	}

	// Download: GET response body.
	downReq := c.newRequest(ctx, http.MethodGet, downURL, nil)
	downResp, err := c.transport.RoundTrip(downReq)
	if err != nil {
		return nil, E.Cause(err, "open download")
	}
	if downResp.StatusCode != http.StatusOK {
		downResp.Body.Close()
		return nil, E.New("v2ray-xhttp: unexpected download status: ", downResp.Status)
	}

	// Upload: streamed POST request body.
	pipeReader, pipeWriter := io.Pipe()
	upReq := c.newRequest(ctx, http.MethodPost, upURL, pipeReader)
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
	downURL, err := c.requestURL(sessionID)
	if err != nil {
		return nil, err
	}
	downReq := c.newRequest(ctx, http.MethodGet, downURL, nil)
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
	if c.reader == nil {
		<-c.created
		if c.readerErr != nil {
			return 0, c.readerErr
		}
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
	closed     bool
}

func (c *packetConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *packetConn) Write(b []byte) (int, error) {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return 0, net.ErrClosed
	}
	seq := c.seq
	c.seq++
	c.access.Unlock()

	u, err := c.client.requestURL(c.sessionID, strconv.FormatUint(seq, 10))
	if err != nil {
		return 0, err
	}
	payload := make([]byte, len(b))
	copy(payload, b)
	request := c.client.newRequest(c.ctx, http.MethodPost, u, &byteReader{data: payload})
	request.Header.Set("Content-Type", "application/octet-stream")
	request.ContentLength = int64(len(payload))

	response, err := c.client.transport.RoundTrip(request)
	if err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return 0, E.New("v2ray-xhttp: unexpected upload status: ", response.Status)
	}
	drainAndClose(response.Body)
	return len(b), nil
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
