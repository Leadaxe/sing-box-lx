package trusttunnel

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/baderror"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
)

const (
	// serverMaxConcurrentStreams and serverIdleTimeout mirror the official
	// endpoint's defaults (max_concurrent_streams, client_listener_timeout_secs).
	serverMaxConcurrentStreams = 1000
	serverIdleTimeout          = 600 * time.Second
	// serverMaxIncomingStreams mirrors the endpoint's initial_max_streams_bidi.
	serverMaxIncomingStreams = 4096

	serverHandshakeTimeout = 15 * time.Second
)

// Metadata describes one tunnelled connection or UDP flow.
type Metadata struct {
	// User is the authenticated username.
	User string
	// Source is the client: the session's remote address, or for UDP the
	// frame's source when the client sent one.
	Source M.Socksaddr
	// Destination is the CONNECT authority (a domain is kept as a domain) or the
	// frame's destination.
	Destination M.Socksaddr
	Network     string
}

// Handler receives the server's tunnelled traffic.
type Handler interface {
	// NewConnection is called for a TCP CONNECT. It must hand conn over and
	// return; the stream stays open until conn is closed. The CONNECT is answered
	// 200 on the first Read or Write of conn, or 502 by Fail.
	NewConnection(ctx context.Context, conn *ServerConn, metadata Metadata)
	// NewPacketConnection is called for each new UDP flow of a _udp2 stream.
	NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata Metadata)
}

type ServiceOptions struct {
	Context context.Context
	Logger  logger.ContextLogger
	Handler Handler
	Users   []auth.User
	// QUIC configures the HTTP/3 transport; nil means BBR.
	QUIC *QUICOptions
}

// Service is the server half: it serves CONNECT over HTTP/2 (ServeTCP) and
// HTTP/3 (ServeQUIC, with_quic only) and hands the tunnelled traffic to the
// Handler.
type Service struct {
	ctx        context.Context
	logger     logger.ContextLogger
	handler    Handler
	users      userTable
	congestion string
	h2Server   *http2.Server
	httpServer *http.Server

	access  sync.Mutex
	closed  bool
	closers map[io.Closer]struct{}
}

func NewService(options ServiceOptions) (*Service, error) {
	if options.Handler == nil {
		return nil, E.New("trusttunnel: missing handler")
	}
	var congestion string
	if options.QUIC != nil {
		congestion = options.QUIC.CongestionControl
	}
	err := validateCongestionControl(congestion)
	if err != nil {
		return nil, err
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	contextLogger := options.Logger
	if contextLogger == nil {
		contextLogger = logger.NOP()
	}
	service := &Service{
		ctx:        ctx,
		logger:     contextLogger,
		handler:    options.Handler,
		congestion: congestion,
		h2Server: &http2.Server{
			MaxConcurrentStreams: serverMaxConcurrentStreams,
			IdleTimeout:          serverIdleTimeout,
		},
		httpServer: &http.Server{
			ErrorLog: log.New(serverLogWriter{contextLogger}, "", 0),
		},
		closers: make(map[io.Closer]struct{}),
	}
	service.users.update(options.Users)
	return service, nil
}

// UpdateUsers replaces the user table; established sessions are not affected,
// new requests are checked against the new table.
func (s *Service) UpdateUsers(users []auth.User) {
	s.users.update(users)
}

// ServeTCP serves HTTP/2 over TLS on the listener until it fails or the service
// is closed. Connections that do not negotiate h2 are closed; HTTP/1.1 is not
// served.
func (s *Service) ServeTCP(listener net.Listener, tlsConfig aTLS.ServerConfig) error {
	if tlsConfig == nil {
		return E.New("trusttunnel: TLS is required")
	}
	if nextProtos := tlsConfig.NextProtos(); !common.Contains(nextProtos, http2.NextProtoTLS) {
		tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, nextProtos...))
	}
	if !s.track(listener) {
		return net.ErrClosed
	}
	defer s.untrack(listener)
	var tempDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.isClosed() {
				return net.ErrClosed
			}
			//nolint:staticcheck
			if netErr, isNetErr := err.(net.Error); isNetErr && netErr.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay = min(tempDelay*2, time.Second)
				}
				s.logger.Error("trusttunnel: accept: ", err, "; retrying in ", tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		tempDelay = 0
		go s.serveHTTP2(conn, tlsConfig)
	}
}

func (s *Service) serveHTTP2(conn net.Conn, tlsConfig aTLS.ServerConfig) {
	if !s.track(conn) {
		conn.Close()
		return
	}
	defer s.untrack(conn)
	defer conn.Close()
	handshakeCtx, cancel := context.WithTimeout(s.ctx, serverHandshakeTimeout)
	tlsConn, err := aTLS.ServerHandshake(handshakeCtx, conn, tlsConfig)
	cancel()
	if err != nil {
		s.logger.DebugContext(s.ctx, "trusttunnel: TLS handshake from ", conn.RemoteAddr(), ": ", err)
		return
	}
	if negotiated := tlsConn.ConnectionState().NegotiatedProtocol; negotiated != http2.NextProtoTLS {
		s.logger.DebugContext(s.ctx, "trusttunnel: closing connection from ", conn.RemoteAddr(), ": ALPN ", negotiated, " is not h2")
		return
	}
	s.h2Server.ServeConn(tlsConn, &http2.ServeConnOpts{
		Context:    s.ctx,
		BaseConfig: s.httpServer,
		Handler:    s,
	})
}

// Close stops every listener and closes every session; streams and UDP flows
// end with them.
func (s *Service) Close() error {
	s.access.Lock()
	if s.closed {
		s.access.Unlock()
		return nil
	}
	s.closed = true
	closers := make([]io.Closer, 0, len(s.closers))
	for closer := range s.closers {
		closers = append(closers, closer)
	}
	s.closers = nil
	s.access.Unlock()
	var errs []error
	for _, closer := range closers {
		err := closer.Close()
		if err != nil && !E.IsClosed(err) {
			errs = append(errs, err)
		}
	}
	return E.Errors(errs...)
}

func (s *Service) track(closer io.Closer) bool {
	s.access.Lock()
	defer s.access.Unlock()
	if s.closed {
		return false
	}
	s.closers[closer] = struct{}{}
	return true
}

func (s *Service) untrack(closer io.Closer) {
	s.access.Lock()
	defer s.access.Unlock()
	delete(s.closers, closer)
}

func (s *Service) isClosed() bool {
	s.access.Lock()
	defer s.access.Unlock()
	return s.closed
}

// ServeHTTP handles one request of an HTTP/2 or HTTP/3 session.
func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	source := M.ParseSocksaddr(request.RemoteAddr).Unwrap()
	if request.Method != http.MethodConnect {
		// A browser or a scanner sees a web server with nothing on it.
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	username, authenticated := s.users.verify(request.Header.Get(headerProxyAuthorization))
	if !authenticated {
		s.logger.ErrorContext(ctx, "trusttunnel: process connection from ", source, ": authentication failed")
		writer.Header().Set(headerProxyAuthenticate, proxyAuthenticateValue)
		writer.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	authority := request.Host
	if authority == "" && request.URL != nil {
		authority = request.URL.Host
	}
	switch authority {
	case AuthorityCheck:
		writer.WriteHeader(http.StatusOK)
		flush(writer)
		return
	case AuthorityICMP:
		writer.WriteHeader(http.StatusNotFound)
		return
	case AuthorityUDP:
		s.serveUDP(ctx, writer, request, username, source)
		return
	}
	destination, err := parseAuthority(authority)
	if err != nil {
		s.logger.DebugContext(ctx, "trusttunnel: process connection from ", source, ": ", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	conn := &ServerConn{
		reader:      request.Body,
		writer:      writer,
		source:      source,
		destination: destination,
		done:        make(chan struct{}),
	}
	if localAddr, loaded := ctx.Value(http.LocalAddrContextKey).(net.Addr); loaded {
		conn.local = M.SocksaddrFromNet(localAddr).Unwrap()
	}
	s.handler.NewConnection(ctx, conn, Metadata{
		User:        username,
		Source:      source,
		Destination: destination,
		Network:     N.NetworkTCP,
	})
	select {
	case <-conn.done:
	case <-ctx.Done():
	}
	// No write may reach the response writer once the handler returns.
	conn.Close()
}

// parseAuthority parses host:port; a missing or zero port is an error.
func parseAuthority(authority string) (M.Socksaddr, error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return M.Socksaddr{}, E.Cause(err, "parse authority")
	}
	destination := M.ParseSocksaddrHostPortStr(host, port)
	if !destination.IsValid() || destination.Port == 0 {
		return M.Socksaddr{}, E.New("invalid authority: ", authority)
	}
	return destination, nil
}

func flush(writer http.ResponseWriter) error {
	return http.NewResponseController(writer).Flush()
}

// ServerConn is a TCP CONNECT stream on the server side. The answer to the
// CONNECT is deferred: 200 goes out on the first Read or Write, 502 with an
// X-Warning on Fail (or on Close before any I/O), so that the client sees a
// failed dial as an error rather than as a dead stream.
type ServerConn struct {
	reader      io.ReadCloser
	writer      http.ResponseWriter
	source      M.Socksaddr
	destination M.Socksaddr
	local       M.Socksaddr

	access    sync.Mutex
	responded bool
	closed    bool
	done      chan struct{}
}

func (c *ServerConn) respondLocked() error {
	if c.responded {
		return nil
	}
	c.responded = true
	c.writer.WriteHeader(http.StatusOK)
	return flush(c.writer)
}

func (c *ServerConn) failLocked(err error) {
	if c.responded {
		return
	}
	c.responded = true
	c.writer.Header().Set(headerWarning, warningFor(err))
	c.writer.WriteHeader(http.StatusBadGateway)
	flush(c.writer)
}

func (c *ServerConn) Read(p []byte) (int, error) {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return 0, net.ErrClosed
	}
	err := c.respondLocked()
	c.access.Unlock()
	if err != nil {
		return 0, wrapWriteError(err)
	}
	n, err := c.reader.Read(p)
	if closedErr := streamClosedError(err); closedErr != nil {
		return n, closedErr
	}
	return n, baderror.WrapH2(err)
}

func (c *ServerConn) Write(p []byte) (int, error) {
	c.access.Lock()
	defer c.access.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	err := c.respondLocked()
	if err != nil {
		return 0, wrapWriteError(err)
	}
	n, err := c.writer.Write(p)
	if err == nil {
		err = flush(c.writer)
	}
	return n, wrapWriteError(err)
}

// wrapWriteError maps a stream the client ended (NO_ERROR, CANCEL and their
// HTTP/3 forms) to net.ErrClosed and other HTTP/2 errors through
// baderror.WrapH2.
func wrapWriteError(err error) error {
	if isStreamClosed(err) {
		return net.ErrClosed
	}
	return baderror.WrapH2(err)
}

// Fail answers the CONNECT with 502 and closes the stream. err selects the
// X-Warning code: 302 for a timeout, 301 for an unreachable host, 300
// otherwise. After the CONNECT was answered it only closes the stream.
func (c *ServerConn) Fail(err error) error {
	c.access.Lock()
	if !c.closed {
		c.failLocked(err)
	}
	c.access.Unlock()
	return c.Close()
}

func (c *ServerConn) Close() error {
	c.access.Lock()
	if c.closed {
		c.access.Unlock()
		return nil
	}
	c.failLocked(nil)
	c.closed = true
	c.access.Unlock()
	close(c.done)
	return c.reader.Close()
}

func (c *ServerConn) LocalAddr() net.Addr {
	return c.local
}

func (c *ServerConn) RemoteAddr() net.Addr {
	return c.source
}

func (c *ServerConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *ServerConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *ServerConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *ServerConn) NeedAdditionalReadDeadline() bool {
	return true
}

type serverLogWriter struct {
	logger logger.ContextLogger
}

func (w serverLogWriter) Write(p []byte) (int, error) {
	w.logger.Debug("trusttunnel: ", strings.TrimSpace(string(p)))
	return len(p), nil
}
