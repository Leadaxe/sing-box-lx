package trusttunnel

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sagernet/sing/common/baderror"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
)

func (c *Client) connectHTTP2(ctx context.Context) (sessionConn, error) {
	conn, err := c.dialer.DialContext(ctx, N.NetworkTCP, c.server)
	if err != nil {
		return nil, E.Cause(err, "dial tcp")
	}
	tlsConn, err := aTLS.ClientHandshake(ctx, conn, c.tlsConfig)
	if err != nil {
		conn.Close()
		return nil, E.Cause(err, "TLS handshake")
	}
	if negotiated := tlsConn.ConnectionState().NegotiatedProtocol; negotiated != http2.NextProtoTLS {
		tlsConn.Close()
		return nil, E.New("server negotiated ALPN ", strconv.Quote(negotiated), ", expected h2")
	}
	transport, err := newHTTP2Transport()
	if err != nil {
		tlsConn.Close()
		return nil, err
	}
	clientConn, err := transport.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, E.Cause(err, "HTTP/2 handshake")
	}
	return &http2Session{conn: clientConn}, nil
}

// Chrome's HTTP/2 connection preface: SETTINGS 1:65536;2:0;4:6291456;6:262144
// and a connection WINDOW_UPDATE of 15663105.
const (
	chromeHeaderTableSize   = 65536
	chromeStreamWindow      = 6291456
	chromeConnectionWindow  = 15663105
	chromeMaxHeaderListSize = 262144
)

// newHTTP2Transport returns the transport whose ClientConn is the session.
//
// The preface follows Chrome as far as x/net/http2 allows without a fork. The
// values are Chrome's: HEADER_TABLE_SIZE 65536, ENABLE_PUSH 0,
// INITIAL_WINDOW_SIZE 6291456, MAX_HEADER_LIST_SIZE 262144, connection
// WINDOW_UPDATE 15663105. What stays Go's: the order of the settings
// (2,4,5,6,1 instead of 1,2,4,6) and MAX_FRAME_SIZE, which x/net always sends;
// it carries 16384, the spec default, because an unset MaxReadFrameSize clips
// to the minimum. The windows are reachable only through net/http's HTTP2Config, hence
// ConfigureTransports; the http.Transport itself never dials. With Go 1.27
// x/net/http2 wraps net/http's implementation; TestHTTP2Preface pins the
// result.
func newHTTP2Transport() (*http2.Transport, error) {
	transport, err := http2.ConfigureTransports(&http.Transport{
		HTTP2: &http.HTTP2Config{
			MaxReceiveBufferPerStream:     chromeStreamWindow,
			MaxReceiveBufferPerConnection: chromeConnectionWindow,
		},
	})
	if err != nil {
		return nil, E.Cause(err, "configure HTTP/2 transport")
	}
	transport.DisableCompression = true
	transport.AllowHTTP = false
	transport.MaxHeaderListSize = chromeMaxHeaderListSize
	transport.MaxDecoderHeaderTableSize = chromeHeaderTableSize
	// One ClientConn is the whole session; the transport's pool is bypassed.
	// Strict: at the server's stream limit a CONNECT waits for a free slot,
	// bounded by its context. Without it the ClientConn reports itself unusable
	// at the limit, which is how the pool decides to open a second connection.
	transport.StrictMaxConcurrentStreams = true
	// No PING: an idle session sends nothing.
	transport.ReadIdleTimeout = 0
	return transport, nil
}

type http2Session struct {
	conn *http2.ClientConn
}

func (s *http2Session) roundTrip(ctx context.Context, authority string, header http.Header, withBody bool) (*rawStream, error) {
	// The stream outlives ctx: ctx only bounds the wait for the answer.
	requestCtx, cancel := context.WithCancel(context.Background())
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: authority},
		Host:   authority,
		Header: header,
	}
	var pipeWriter *io.PipeWriter
	if withBody {
		var pipeReader *io.PipeReader
		pipeReader, pipeWriter = io.Pipe()
		request.Body = pipeReader
	}
	request = request.WithContext(requestCtx)
	stop := context.AfterFunc(ctx, cancel)
	response, err := s.conn.RoundTrip(request)
	if !stop() {
		if err == nil {
			response.Body.Close()
		}
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		if pipeWriter != nil {
			pipeWriter.Close()
		}
		return nil, err
	}
	stream := &rawStream{
		response: response,
		reader:   response.Body,
		closeFunc: func() {
			if pipeWriter != nil {
				pipeWriter.Close()
			}
			response.Body.Close()
			cancel()
		},
		wrapError: baderror.WrapH2,
	}
	if pipeWriter != nil {
		stream.writer = pipeWriter
	}
	return stream, nil
}

func (s *http2Session) alive() bool {
	state := s.conn.State()
	return !state.Closed && !state.Closing
}

func (s *http2Session) close() error {
	return s.conn.Close()
}
