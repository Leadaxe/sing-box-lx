package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

const (
	ProtocolHTTP2 = "h2"
	ProtocolHTTP3 = "h3"
)

const (
	// DefaultStaleWindow is how long a session may stay silent before the next
	// CONNECT on it is treated as a probe, when no idle timeout is set.
	DefaultStaleWindow = 5 * time.Minute

	// staleConnectBudget bounds a CONNECT on a stale session, so that a session
	// whose path died while idle (NAT expiry, sleep) costs seconds, not the
	// caller's whole deadline, before it is rebuilt.
	staleConnectBudget = 3 * time.Second

	// healthCheckThrottle is the silence after which a dial sends _check first;
	// it is also the minimum distance between two checks.
	healthCheckThrottle = 30 * time.Second

	// healthCheckTimeout is the response deadline of _check.
	healthCheckTimeout = 7 * time.Second

	defaultAppName = "unknown"
)

// ErrQUICNotIncluded is returned for HTTP/3 in a build without with_quic.
var ErrQUICNotIncluded = E.New("trusttunnel: HTTP/3 requires the with_quic build tag")

// QUICOptions configures the HTTP/3 transport.
type QUICOptions struct {
	// CongestionControl is "bbr" (default), "cubic" or "new_reno" ("reno" is
	// accepted as an alias).
	CongestionControl string
}

type ClientOptions struct {
	Context context.Context
	Logger  logger.ContextLogger
	// Dialer opens the TCP connection for HTTP/2 and the UDP socket for HTTP/3.
	Dialer N.Dialer
	Server M.Socksaddr
	// TLSConfig is the client TLS configuration. Its ALPN, when set, must contain
	// the protocol in use; when empty it is set to it. HTTP/3 needs a
	// configuration that yields a standard *tls.Config.
	TLSConfig aTLS.Config
	// Protocol is ProtocolHTTP2 (default) or ProtocolHTTP3.
	Protocol string
	QUIC     *QUICOptions
	Username string
	Password string
	// AppName is the application string sent in the user agent and in every UDP
	// frame; "unknown" when empty.
	AppName string
	// HealthCheck enables CONNECT _check before a dial on a session that has
	// been silent for longer than 30 s. It is never sent on a timer.
	HealthCheck bool
	// IdleTimeout closes the session locally after this long without activity
	// and without open streams; zero keeps it until the server or the network
	// ends it.
	IdleTimeout time.Duration
}

// Client is a TrustTunnel client: one lazily opened session to the server,
// carrying every CONNECT stream.
type Client struct {
	ctx            context.Context
	logger         logger.ContextLogger
	dialer         N.Dialer
	server         M.Socksaddr
	tlsConfig      aTLS.Config
	protocol       string
	congestion     string
	authorization  string
	appName        string
	userAgent      string
	userAgentUDP   string
	userAgentCheck string
	healthCheck    bool
	idleTimeout    time.Duration
	staleWindow    time.Duration

	access  sync.Mutex
	session *clientSession
	closed  bool
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.Dialer == nil {
		return nil, E.New("trusttunnel: missing dialer")
	}
	if !options.Server.IsValid() {
		return nil, E.New("trusttunnel: missing server address")
	}
	if options.TLSConfig == nil {
		return nil, E.New("trusttunnel: TLS is required")
	}
	if options.Username == "" {
		return nil, E.New("trusttunnel: missing username")
	}
	appName := options.AppName
	if appName == "" {
		appName = defaultAppName
	}
	if len(appName) > MaxAppNameSize {
		return nil, E.New("trusttunnel: application name longer than ", MaxAppNameSize, " bytes")
	}
	protocol := options.Protocol
	if protocol == "" {
		protocol = ProtocolHTTP2
	}
	var congestion string
	switch protocol {
	case ProtocolHTTP2:
	case ProtocolHTTP3:
		if !quicIncluded {
			return nil, ErrQUICNotIncluded
		}
		if options.QUIC != nil {
			congestion = options.QUIC.CongestionControl
		}
		err := validateCongestionControl(congestion)
		if err != nil {
			return nil, err
		}
	default:
		return nil, E.New("trusttunnel: unknown protocol: ", protocol)
	}
	tlsConfig := options.TLSConfig.Clone()
	if nextProtos := tlsConfig.NextProtos(); len(nextProtos) == 0 {
		tlsConfig.SetNextProtos([]string{protocol})
	} else if !common.Contains(nextProtos, protocol) {
		return nil, E.New("trusttunnel: TLS ALPN must contain ", protocol)
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	contextLogger := options.Logger
	if contextLogger == nil {
		contextLogger = logger.NOP()
	}
	staleWindow := options.IdleTimeout
	if staleWindow <= 0 {
		staleWindow = DefaultStaleWindow
	}
	return &Client{
		ctx:            ctx,
		logger:         contextLogger,
		dialer:         options.Dialer,
		server:         options.Server,
		tlsConfig:      tlsConfig,
		protocol:       protocol,
		congestion:     congestion,
		authorization:  BasicAuthorization(options.Username, options.Password),
		appName:        appName,
		userAgent:      UserAgent(appName),
		userAgentUDP:   UserAgentUDP(),
		userAgentCheck: UserAgentCheck(),
		healthCheck:    options.HealthCheck,
		idleTimeout:    max(options.IdleTimeout, 0),
		staleWindow:    staleWindow,
	}, nil
}

// DialContext opens CONNECT host:port and returns after the server's answer: a
// connection on 200, a *StatusError otherwise. A domain destination is sent
// unresolved; the server resolves it.
func (c *Client) DialContext(ctx context.Context, destination M.Socksaddr) (net.Conn, error) {
	if !destination.IsValid() || destination.Port == 0 {
		return nil, E.New("trusttunnel: invalid destination: ", destination)
	}
	session, stream, err := c.openStream(ctx, destination.String(), c.userAgent, true)
	if err != nil {
		return nil, err
	}
	return newStreamConn(session, stream, c.server, destination), nil
}

// ListenPacket opens one _udp2 stream. Datagrams on it carry IP addresses only;
// the caller resolves domains.
func (c *Client) ListenPacket(ctx context.Context) (*PacketConn, error) {
	session, stream, err := c.openStream(ctx, AuthorityUDP, c.userAgentUDP, true)
	if err != nil {
		return nil, err
	}
	return newPacketConn(newStreamConn(session, stream, c.server, c.server), c.appName, c.server), nil
}

// Check sends CONNECT _check on the session, opening it if needed. A failed
// check tears the session down.
func (c *Client) Check(ctx context.Context) error {
	session, _, err := c.getSession(ctx)
	if err != nil {
		return err
	}
	return c.check(ctx, session)
}

// Reset tears the session down; the next request opens a new one. Meant for
// network changes: QUIC migration is off on the official endpoint.
func (c *Client) Reset() {
	c.access.Lock()
	session := c.session
	c.session = nil
	c.access.Unlock()
	if session != nil {
		session.close()
	}
}

func (c *Client) Close() error {
	c.access.Lock()
	c.closed = true
	session := c.session
	c.session = nil
	c.access.Unlock()
	if session != nil {
		session.close()
	}
	return nil
}

func (c *Client) openStream(ctx context.Context, authority string, userAgent string, withBody bool) (*clientSession, *rawStream, error) {
	session, fresh, err := c.getSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !fresh && c.healthCheck && session.checkDue() {
		err = c.check(ctx, session)
		if err != nil {
			if ctx.Err() != nil || !retryable(err) {
				return nil, nil, err
			}
			c.logger.DebugContext(ctx, "trusttunnel: health check of session to ", c.server, " failed, reconnecting: ", err)
			session, fresh, err = c.getSession(ctx)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	stream, err := c.roundTrip(ctx, session, authority, userAgent, withBody, !fresh)
	if err == nil || fresh || ctx.Err() != nil || !retryable(err) {
		return session, stream, err
	}
	c.logger.DebugContext(ctx, "trusttunnel: CONNECT on session to ", c.server, " failed, reconnecting: ", err)
	session, _, err = c.getSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	stream, err = c.roundTrip(ctx, session, authority, userAgent, withBody, false)
	return session, stream, err
}

// roundTrip sends one CONNECT on the session and checks the answer. A transport
// failure or an authentication refusal tears the session down; a CONNECT that
// may land on a stale session is bounded by staleConnectBudget.
func (c *Client) roundTrip(ctx context.Context, session *clientSession, authority string, userAgent string, withBody bool, mayBeStale bool) (*rawStream, error) {
	attemptCtx := ctx
	if mayBeStale && session.silentFor() > c.staleWindow {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, staleConnectBudget)
		defer cancel()
	}
	stream, err := session.conn.roundTrip(attemptCtx, authority, c.requestHeader(userAgent), withBody)
	if err != nil {
		if ctx.Err() == nil {
			c.teardown(session)
		}
		if IsAuthenticationRequired(err) {
			return nil, E.Cause1(ErrAuthenticationRequired, err)
		}
		return nil, E.Cause(err, "trusttunnel: CONNECT ", authority)
	}
	if stream.response.StatusCode != http.StatusOK {
		stream.close()
		statusError := NewStatusError(stream.response.StatusCode, stream.response.Header)
		if statusError.refusesSession() {
			c.logger.DebugContext(ctx, "trusttunnel: server ", c.server, " refused the credentials (", statusError.StatusCode, "), closing session")
			c.teardown(session)
		} else {
			session.markReceived()
		}
		return nil, statusError
	}
	session.markReceived()
	session.streams.Add(1)
	return stream, nil
}

func (c *Client) check(ctx context.Context, session *clientSession) error {
	checkCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	session.checked.Store(time.Now().UnixNano())
	stream, err := session.conn.roundTrip(checkCtx, AuthorityCheck, c.requestHeader(c.userAgentCheck), false)
	if err != nil {
		if ctx.Err() == nil {
			c.teardown(session)
		}
		if IsAuthenticationRequired(err) {
			return E.Cause1(ErrAuthenticationRequired, err)
		}
		return E.Cause(err, "trusttunnel: health check")
	}
	stream.close()
	if stream.response.StatusCode != http.StatusOK {
		c.teardown(session)
		return NewStatusError(stream.response.StatusCode, stream.response.Header)
	}
	session.markReceived()
	return nil
}

func (c *Client) requestHeader(userAgent string) http.Header {
	return http.Header{
		headerProxyAuthorization: []string{c.authorization},
		headerUserAgent:          []string{userAgent},
	}
}

// retryable reports whether a failed CONNECT may be repeated on a new session:
// any transport failure, but not an answer of the server.
func retryable(err error) bool {
	var statusError *StatusError
	return !errors.As(err, &statusError) && !IsAuthenticationRequired(err)
}

// getSession returns the live session, opening one when there is none. fresh
// reports that the session was opened by this call.
func (c *Client) getSession(ctx context.Context) (*clientSession, bool, error) {
	c.access.Lock()
	defer c.access.Unlock()
	if c.closed {
		return nil, false, net.ErrClosed
	}
	if session := c.session; session != nil {
		if session.conn.alive() {
			session.markActivity()
			return session, false, nil
		}
		c.session = nil
		session.close()
		c.logger.DebugContext(ctx, "trusttunnel: session to ", c.server, " is closed")
	}
	c.logger.DebugContext(ctx, "trusttunnel: opening ", c.protocol, " session to ", c.server)
	var (
		conn sessionConn
		err  error
	)
	switch c.protocol {
	case ProtocolHTTP3:
		conn, err = c.connectQUIC(ctx)
	default:
		conn, err = c.connectHTTP2(ctx)
	}
	if err != nil {
		return nil, false, E.Cause(err, "trusttunnel: open session to ", c.server)
	}
	session := newClientSession(c.ctx, conn)
	c.session = session
	if c.idleTimeout > 0 {
		go c.idleWatcher(session)
	}
	c.logger.DebugContext(ctx, "trusttunnel: ", c.protocol, " session to ", c.server, " opened")
	return session, true, nil
}

func (c *Client) teardown(session *clientSession) {
	c.access.Lock()
	if c.session == session {
		c.session = nil
	}
	c.access.Unlock()
	session.close()
}

// idleWatcher closes the session after idleTimeout without activity and without
// open streams. It lives as long as the session and sends nothing.
func (c *Client) idleWatcher(session *clientSession) {
	interval := max(c.idleTimeout/4, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-session.ctx.Done():
			return
		case <-ticker.C:
			if session.streams.Load() > 0 {
				continue
			}
			idleFor := time.Duration(time.Now().UnixNano() - session.activity.Load())
			if idleFor >= c.idleTimeout {
				c.logger.DebugContext(c.ctx, "trusttunnel: closing session to ", c.server, " after ", idleFor.Round(time.Second), " idle")
				c.teardown(session)
				return
			}
		}
	}
}

// sessionConn is one HTTP/2 or HTTP/3 connection to the server.
type sessionConn interface {
	// roundTrip sends CONNECT authority and returns once the response headers
	// arrived, whatever the status. ctx bounds the wait for a stream slot and for
	// the answer, not the life of the stream.
	roundTrip(ctx context.Context, authority string, header http.Header, withBody bool) (*rawStream, error)
	alive() bool
	close() error
}

// rawStream is an answered CONNECT stream.
type rawStream struct {
	response  *http.Response
	reader    io.Reader
	writer    io.Writer
	closeFunc func()
	deadline  deadlineSetter
	wrapError func(error) error
	closeOnce sync.Once
}

func (s *rawStream) close() {
	s.closeOnce.Do(s.closeFunc)
}

type deadlineSetter interface {
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

type clientSession struct {
	conn      sessionConn
	ctx       context.Context
	cancel    context.CancelFunc
	activity  atomic.Int64
	received  atomic.Int64
	checked   atomic.Int64
	streams   atomic.Int64
	closeOnce sync.Once
}

func newClientSession(ctx context.Context, conn sessionConn) *clientSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &clientSession{
		conn:   conn,
		ctx:    sessionCtx,
		cancel: cancel,
	}
	session.markReceived()
	return session
}

func (s *clientSession) markActivity() {
	s.activity.Store(time.Now().UnixNano())
}

func (s *clientSession) markReceived() {
	now := time.Now().UnixNano()
	s.activity.Store(now)
	s.received.Store(now)
}

// silentFor is the time since the server last proved the session alive: data
// received or a request answered.
func (s *clientSession) silentFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - s.received.Load())
}

// checkDue reports whether a health check may be sent: the session has been
// silent for the throttle and no check was sent within it.
func (s *clientSession) checkDue() bool {
	last := max(s.received.Load(), s.checked.Load())
	return time.Duration(time.Now().UnixNano()-last) > healthCheckThrottle
}

func (s *clientSession) close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.conn.close()
	})
}

// streamConn is a CONNECT stream as a net.Conn.
type streamConn struct {
	session    *clientSession
	stream     *rawStream
	localAddr  M.Socksaddr
	remoteAddr M.Socksaddr
	closeOnce  sync.Once
}

func newStreamConn(session *clientSession, stream *rawStream, localAddr M.Socksaddr, remoteAddr M.Socksaddr) *streamConn {
	return &streamConn{
		session:    session,
		stream:     stream,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}
}

func (c *streamConn) Read(p []byte) (int, error) {
	n, err := c.stream.reader.Read(p)
	if n > 0 {
		c.session.markReceived()
	}
	return n, c.stream.wrapError(err)
}

func (c *streamConn) Write(p []byte) (int, error) {
	n, err := c.stream.writer.Write(p)
	c.session.markActivity()
	return n, c.stream.wrapError(err)
}

func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		c.stream.close()
		c.session.streams.Add(-1)
		c.session.markActivity()
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *streamConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *streamConn) SetDeadline(t time.Time) error {
	if c.stream.deadline == nil {
		return os.ErrInvalid
	}
	err := c.stream.deadline.SetReadDeadline(t)
	if err != nil {
		return err
	}
	return c.stream.deadline.SetWriteDeadline(t)
}

func (c *streamConn) SetReadDeadline(t time.Time) error {
	if c.stream.deadline == nil {
		return os.ErrInvalid
	}
	return c.stream.deadline.SetReadDeadline(t)
}

func (c *streamConn) SetWriteDeadline(t time.Time) error {
	if c.stream.deadline == nil {
		return os.ErrInvalid
	}
	return c.stream.deadline.SetWriteDeadline(t)
}

func (c *streamConn) NeedAdditionalReadDeadline() bool {
	return c.stream.deadline == nil
}
