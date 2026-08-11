package v2rayxhttp

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/net/http2"
)

// XMUX — reuse of HTTP connections across XHTTP dials. Ported from
// sing-box-extended (transport/v2rayxhttp/mux.go, tag v1.13.18-extended-2.6.4)
// over Xray's XmuxConfig; see SPECS/TASKS/059-XHTTP_XMUX for the contract and
// REFERENCE_mux.go.txt in that folder for the source we mirrored.
//
// Nothing here changes what goes on the wire: the server identifies a session by
// its session id, not by the connection carrying it, so pooling is purely a
// client-side policy. What it buys is (a) behaving like an Xray client, whose
// xmux section subscription configs routinely carry, and (b) not paying a fresh
// TCP+TLS(+Reality) handshake for every stream.

// xmuxConn is the poolable resource: one HTTP connection (for us, one
// *http2.Transport with its own dialer). It is an interface so tests can pool
// fakes instead of real transports.
type xmuxConn interface {
	// Close releases the underlying connection.
	Close()
	// IsClosed reports whether the connection died on its own (transport error,
	// server GOAWAY); such connections are evicted on the next pool lookup.
	IsClosed() bool
	// roundTripper returns the RoundTripper carrying this connection's requests.
	roundTripper() http.RoundTripper
}

// xmuxClient wraps one pooled connection with the counters that decide when it
// is retired.
//
// Closing is DEFERRED: Close on a connection that still carries streams only
// marks it, and the real teardown happens when the last stream releases it via
// addOpenUsage(-1). Retiring a connection must never tear down traffic the user
// is still running through it.
type xmuxClient struct {
	conn         xmuxConn
	openUsage    int   // streams currently alive on this connection
	leftUsage    int   // handouts left before retirement; -1 = unlimited
	leftRequests int32 // HTTP requests left before retirement (atomic)
	unreusableAt time.Time

	closed bool
	access sync.Mutex
}

// close marks the connection retired, tearing it down immediately only if no
// stream is using it.
func (c *xmuxClient) close() {
	c.access.Lock()
	defer c.access.Unlock()
	c.closed = true
	if c.openUsage <= 0 {
		c.conn.Close()
	}
}

// addOpenUsage adjusts the live-stream count, tearing the connection down when
// the last stream leaves an already-retired connection.
func (c *xmuxClient) addOpenUsage(delta int) {
	c.access.Lock()
	defer c.access.Unlock()
	c.openUsage += delta
	if c.closed && c.openUsage <= 0 {
		c.conn.Close()
	}
}

func (c *xmuxClient) getOpenUsage() int {
	c.access.Lock()
	defer c.access.Unlock()
	return c.openUsage
}

// takeRequest accounts one HTTP request against h_max_request_times. It is
// called per REQUEST, not per stream: a packet-up stream issues one upload POST
// per Write, and a connection that ignored those would outlive the limit the
// server was told about.
func (c *xmuxClient) takeRequest() {
	atomic.AddInt32(&c.leftRequests, -1)
}

// http2XmuxConn adapts an *http2.Transport to the pool's resource interface.
//
// http2.Transport has no "is it dead" signal of its own — it silently redials
// on the next request — so liveness is tracked by us: the transport is
// considered closed once we tore it down. That is enough for the pool, whose
// other three eviction rules (reuse, requests, age) do the real rotation work.
type http2XmuxConn struct {
	transport *http2.Transport
	closed    atomic.Bool
}

func (c *http2XmuxConn) Close() {
	if c.closed.Swap(true) {
		return
	}
	c.transport.CloseIdleConnections()
}

func (c *http2XmuxConn) IsClosed() bool {
	return c.closed.Load()
}

func (c *http2XmuxConn) roundTripper() http.RoundTripper {
	return c.transport
}

// roundTrip issues one HTTP request over this pooled connection, accounting it
// against h_max_request_times. Every XHTTP request must go through here — a
// request that bypassed it would let the connection outlive the limit.
func (c *xmuxClient) roundTrip(request *http.Request) (*http.Response, error) {
	c.takeRequest()
	return c.conn.roundTripper().RoundTrip(request)
}

// xmuxRelease returns the pooled connection to the pool exactly once, however
// many times the conn's Close is called.
//
// Idempotency is not decoration: our conns are closed twice in real paths (an
// expired deadline closing the read half plus the caller's Close, see
// SPECS/TASKS/050), and a double release would drive openUsage negative — the
// connection would then never be torn down after retirement.
type xmuxRelease struct {
	client *xmuxClient
	once   sync.Once
}

func newXmuxRelease(client *xmuxClient) *xmuxRelease {
	return &xmuxRelease{client: client}
}

func (r *xmuxRelease) release() {
	if r == nil || r.client == nil {
		return
	}
	r.once.Do(func() {
		r.client.addOpenUsage(-1)
	})
}

// roundTrip issues a request over the held connection. packetConn keeps its
// release handle for the lifetime of the stream and sends every upload POST
// through it, so uploads are accounted like any other request.
func (r *xmuxRelease) roundTrip(request *http.Request) (*http.Response, error) {
	return r.client.roundTrip(request)
}

// singleTransportXmux builds a manager permanently bound to one RoundTripper.
// It is how a Client is assembled around a caller-supplied transport (tests,
// and any future path that wants to drive XHTTP over a fixed transport): the
// pool degenerates to a single connection that is never rotated.
func singleTransportXmux(transport http.RoundTripper) *xmuxManager {
	conn := &fixedXmuxConn{transport: transport}
	manager := newXmuxManager(xmuxConfig{}, func() xmuxConn { return conn })
	return manager
}

// fixedXmuxConn is a pool resource wrapping an already-built RoundTripper.
type fixedXmuxConn struct {
	transport http.RoundTripper
	closed    atomic.Bool
}

func (c *fixedXmuxConn) Close()                          { c.closed.Store(true) }
func (c *fixedXmuxConn) IsClosed() bool                  { return c.closed.Load() }
func (c *fixedXmuxConn) roundTripper() http.RoundTripper { return c.transport }

// xmuxConfig is the resolved (parsed, defaulted) XMUX option set.
type xmuxConfig struct {
	maxConcurrency   intRange
	maxConnections   intRange
	cMaxReuseTimes   intRange
	hMaxRequestTimes intRange
	hMaxReusableSecs intRange
	keepAlivePeriod  time.Duration
}

// xmuxManager owns the connection pool.
type xmuxManager struct {
	config      xmuxConfig
	concurrency int // rolled once at construction
	connections int // rolled once at construction
	newConn     func() xmuxConn
	clients     []*xmuxClient
	access      sync.Mutex
	// onEvent, when set, receives debug lines about pool activity: connection
	// opened, connection evicted (with cause). Stands in for pool metrics — the
	// pool state is fully determined by the config, so what is worth observing is
	// the transitions, not the gauge. See SPECS/TASKS/059 §8.2.
	onEvent func(format string, args ...any)
}

func newXmuxManager(config xmuxConfig, newConn func() xmuxConn) *xmuxManager {
	return &xmuxManager{
		config:      config,
		concurrency: config.maxConcurrency.rand(),
		connections: config.maxConnections.rand(),
		newConn:     newConn,
		clients:     make([]*xmuxClient, 0),
	}
}

func (m *xmuxManager) logf(format string, args ...any) {
	if m.onEvent != nil {
		m.onEvent(format, args...)
	}
}

// Close retires every pooled connection. Live streams keep theirs alive until
// they finish (deferred close).
func (m *xmuxManager) Close() {
	m.access.Lock()
	clients := m.clients
	m.clients = nil
	m.access.Unlock()
	for _, client := range clients {
		client.close()
	}
}

// newClientLocked opens a connection and rolls its per-connection limits. Each
// range is rolled ONCE, here — not per request.
//
// Caller must hold m.access.
func (m *xmuxManager) newClientLocked() *xmuxClient {
	client := &xmuxClient{
		conn:      m.newConn(),
		leftUsage: -1,
	}
	if x := m.config.cMaxReuseTimes.rand(); x > 0 {
		client.leftUsage = x - 1
	}
	client.leftRequests = maxInt32
	if x := m.config.hMaxRequestTimes.rand(); x > 0 {
		client.leftRequests = int32(x)
	}
	if x := m.config.hMaxReusableSecs.rand(); x > 0 {
		client.unreusableAt = timeNow().Add(time.Duration(x) * time.Second)
	}
	m.clients = append(m.clients, client)
	m.logf("xmux: opened connection (pool=%d, left_usage=%d, left_requests=%d)",
		len(m.clients), client.leftUsage, atomic.LoadInt32(&client.leftRequests))
	return client
}

// evictCause reports why a pooled connection must be retired, or "" if it may
// still be used.
func (c *xmuxClient) evictCause() string {
	switch {
	case c.conn.IsClosed():
		return "closed"
	case c.leftUsage == 0:
		return "reuse"
	case atomic.LoadInt32(&c.leftRequests) <= 0:
		return "requests"
	case !c.unreusableAt.IsZero() && timeNow().After(c.unreusableAt):
		return "expired"
	}
	return ""
}

// get returns a connection for a new stream, opening one when the pool cannot
// serve it. The caller MUST pair this with addOpenUsage(-1) exactly once, via
// the release function returned by Client.acquireXmux.
func (m *xmuxManager) get() *xmuxClient {
	m.access.Lock()
	defer m.access.Unlock()

	var evicted []*xmuxClient
	for i := 0; i < len(m.clients); {
		client := m.clients[i]
		if cause := client.evictCause(); cause != "" {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			evicted = append(evicted, client)
			m.logf("xmux: evicted connection (cause=%s, pool=%d)", cause, len(m.clients))
		} else {
			i++
		}
	}
	// Deferred close: an evicted connection with live streams stays up until they
	// finish, so rotation never cuts traffic mid-flight.
	for _, client := range evicted {
		client.close()
	}

	if len(m.clients) == 0 {
		return m.newClientLocked()
	}
	if m.connections > 0 && len(m.clients) < m.connections {
		return m.newClientLocked()
	}

	candidates := m.clients
	if m.concurrency > 0 {
		candidates = make([]*xmuxClient, 0, len(m.clients))
		for _, client := range m.clients {
			if client.getOpenUsage() < m.concurrency {
				candidates = append(candidates, client)
			}
		}
	}
	if len(candidates) == 0 {
		return m.newClientLocked()
	}

	client := candidates[randIntn(len(candidates))]
	if client.leftUsage > 0 {
		client.leftUsage--
	}
	return client
}

const maxInt32 = int32(^uint32(0) >> 1)

// Defaults mirror sing-box-extended (option/v2ray_transport.go:312-319), which
// applies them whenever the xmux section is absent. Keeping that behaviour is
// the point of the task: a config that says nothing about xmux should still
// produce an Xray-shaped connection pattern.
var (
	defaultXmuxMaxConcurrency   = intRange{1, 1}
	defaultXmuxHMaxRequestTimes = intRange{600, 900}
	defaultXmuxHMaxReusableSecs = intRange{1800, 3000}
)

// normalizeXmux resolves the XMUX option set. A nil section is NOT "disabled" —
// it selects the defaults above.
func normalizeXmux(options *option.V2RayXHTTPXmuxOptions) (xmuxConfig, error) {
	if options == nil {
		return xmuxConfig{
			maxConcurrency:   defaultXmuxMaxConcurrency,
			hMaxRequestTimes: defaultXmuxHMaxRequestTimes,
			hMaxReusableSecs: defaultXmuxHMaxReusableSecs,
		}, nil
	}
	var (
		config xmuxConfig
		err    error
	)
	config.maxConcurrency, err = parseRangeOr(string(options.MaxConcurrency), "xmux.max_concurrency", intRange{})
	if err != nil {
		return xmuxConfig{}, err
	}
	config.maxConnections, err = parseRangeOr(string(options.MaxConnections), "xmux.max_connections", intRange{})
	if err != nil {
		return xmuxConfig{}, err
	}
	// Mutually exclusive, matching the reference: one bounds streams per
	// connection, the other bounds connections, and honouring both at once has no
	// coherent meaning.
	if config.maxConnections.max > 0 && config.maxConcurrency.max > 0 {
		return xmuxConfig{}, E.New("v2ray-xhttp: xmux.max_connections cannot be specified together with xmux.max_concurrency")
	}
	config.cMaxReuseTimes, err = parseRangeOr(string(options.CMaxReuseTimes), "xmux.c_max_reuse_times", intRange{})
	if err != nil {
		return xmuxConfig{}, err
	}
	config.hMaxRequestTimes, err = parseRangeOr(string(options.HMaxRequestTimes), "xmux.h_max_request_times", defaultXmuxHMaxRequestTimes)
	if err != nil {
		return xmuxConfig{}, err
	}
	config.hMaxReusableSecs, err = parseRangeOr(string(options.HMaxReusableSecs), "xmux.h_max_reusable_secs", defaultXmuxHMaxReusableSecs)
	if err != nil {
		return xmuxConfig{}, err
	}
	// Zero means "transport default"; negative disables keep-alive pings.
	config.keepAlivePeriod = time.Duration(options.HKeepAlivePeriod) * time.Second
	return config, nil
}
