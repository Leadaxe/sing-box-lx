package v2rayxhttp

import (
	"context"
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
	manager      *xmuxManager // breaker trips report here; never nil for pooled clients
	openUsage    int          // streams currently alive on this connection
	leftUsage    int          // handouts left before retirement; -1 = unlimited
	leftRequests int32        // HTTP requests left before retirement (atomic)
	unreusableAt time.Time

	// lx: SPEC 076 — circuit breaker. consecFails counts consecutive stream
	// failures (failed round trip, non-200 response, remote death of a download
	// body); any genuine success resets it. At xmuxBreakerThreshold the
	// connection is marked failing and evicted on the next pool lookup.
	consecFails atomic.Int32
	failing     atomic.Bool

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

// lx: SPEC 076 — circuit-breaker knobs. Variables, not consts: tests shrink
// the timings. Field case behind them: a CDN resetting every upload stream
// span an unthrottled dial→reset→dial loop at channel speed and pinned both
// cores of an ARM64 router until the core was restarted (issue #14).
var (
	// xmuxBreakerThreshold is how many CONSECUTIVE stream failures retire the
	// pooled connection.
	xmuxBreakerThreshold = int32(3)
	// xmuxBackoffInitial/xmuxBackoffCap bound the new-transport backoff window:
	// each breaker trip doubles the delay, any genuine success resets it.
	xmuxBackoffInitial = 100 * time.Millisecond
	xmuxBackoffCap     = 3 * time.Second
)

// noteFailure records one stream failure. Crossing the threshold marks the
// connection failing (evicted on the next pool lookup) and arms/extends the
// manager's new-transport backoff. lx: SPEC 076.
func (c *xmuxClient) noteFailure() {
	if c.consecFails.Add(1) != xmuxBreakerThreshold {
		return
	}
	c.failing.Store(true)
	if c.manager != nil {
		c.manager.noteBreakerTrip()
	}
}

// noteSuccess records genuine data flow (a drained 200 upload POST, a first
// successful download-body read) — headers alone don't count, the field case
// had 200-raises whose bodies died seconds later. Resets the failure streak
// and disarms the manager backoff. lx: SPEC 076.
func (c *xmuxClient) noteSuccess() {
	c.consecFails.Store(0)
	if c.manager != nil {
		c.manager.resetBackoff()
	}
}

// roundTrip issues one HTTP request over this pooled connection, accounting it
// against h_max_request_times. Every XHTTP request must go through here — a
// request that bypassed it would let the connection outlive the limit.
//
// It also feeds the breaker (lx: SPEC 076): an error or a non-200 status is a
// stream failure — every XHTTP endpoint expects exactly 200, all call sites
// treat anything else as fatal for the stream. Success is deliberately NOT
// noted here: response headers arriving says nothing about the stream living.
func (c *xmuxClient) roundTrip(request *http.Request) (*http.Response, error) {
	c.takeRequest()
	response, err := c.conn.roundTripper().RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK {
		c.noteFailure()
	}
	return response, err
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

// noteSuccess/noteFailure forward stream-health observations from the conns
// (download-body reads, drained upload POSTs) to the pooled connection's
// breaker. Nil-safe like release. lx: SPEC 076.
func (r *xmuxRelease) noteSuccess() {
	if r == nil || r.client == nil {
		return
	}
	r.client.noteSuccess()
}

func (r *xmuxRelease) noteFailure() {
	if r == nil || r.client == nil {
		return
	}
	r.client.noteFailure()
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

	// lx: SPEC 076 — new-transport backoff, armed by breaker trips. backoffDelay
	// doubles per trip within [xmuxBackoffInitial, xmuxBackoffCap]; blockedUntil
	// is the moment before which get() refuses to OPEN a transport (an existing
	// pooled connection is still handed out freely). backoffArmed mirrors
	// "delay != 0" atomically so resetBackoff on every successful read stays a
	// single atomic load in the healthy case. All under m.access except the gate.
	backoffDelay time.Duration
	blockedUntil time.Time
	backoffArmed atomic.Bool
}

// noteBreakerTrip doubles the new-transport backoff window. lx: SPEC 076.
func (m *xmuxManager) noteBreakerTrip() {
	m.access.Lock()
	if m.backoffDelay == 0 {
		m.backoffDelay = xmuxBackoffInitial
	} else if m.backoffDelay *= 2; m.backoffDelay > xmuxBackoffCap {
		m.backoffDelay = xmuxBackoffCap
	}
	m.blockedUntil = timeNow().Add(m.backoffDelay)
	m.backoffArmed.Store(true)
	delay := m.backoffDelay
	m.access.Unlock()
	m.logf("xmux: breaker tripped, new-transport backoff %s", delay)
}

// resetBackoff disarms the backoff window on genuine success. lx: SPEC 076.
func (m *xmuxManager) resetBackoff() {
	if !m.backoffArmed.Load() {
		return
	}
	m.access.Lock()
	m.backoffDelay = 0
	m.blockedUntil = time.Time{}
	m.backoffArmed.Store(false)
	m.access.Unlock()
	m.logf("xmux: breaker backoff reset")
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
		manager:   m,
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
	// lx: SPEC 076 — the breaker tripped on this connection: it failed
	// xmuxBreakerThreshold streams in a row and must not carry new ones.
	case c.failing.Load():
		return "failing"
	case c.leftUsage == 0:
		return "reuse"
	case atomic.LoadInt32(&c.leftRequests) <= 0:
		return "requests"
	case !c.unreusableAt.IsZero() && timeNow().After(c.unreusableAt):
		return "expired"
	}
	return ""
}

// getContext returns a connection for a new stream, waiting out the breaker's
// new-transport backoff window when opening one is required (lx: SPEC 076). A
// pooled connection is returned without waiting; only the open-a-transport
// path is throttled, so the dial storm the breaker exists for is damped while
// healthy reuse stays untouched.
func (m *xmuxManager) getContext(ctx context.Context) (*xmuxClient, error) {
	for {
		client, wait := m.get()
		if client != nil {
			return client, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// get returns a connection for a new stream, opening one when the pool cannot
// serve it — unless the pool is empty inside the breaker's backoff window, in
// which case it returns (nil, remaining wait) and the caller must wait and
// retry (lx: SPEC 076; getContext does exactly that). The caller MUST pair a
// returned client with addOpenUsage(-1) exactly once, via the release function
// returned by Client.acquireXmux.
func (m *xmuxManager) get() (*xmuxClient, time.Duration) {
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
		// lx: SPEC 076 — the only backoff-gated branch: an empty pool during the
		// window means the breaker just retired everything, and opening straight
		// away is what the dial storm is made of. The non-empty branches below
		// stay ungated: they only run when a live connection exists, and stalling
		// healthy dials over trailing errors would be worse than one extra conn.
		if m.backoffArmed.Load() {
			if wait := m.blockedUntil.Sub(timeNow()); wait > 0 {
				return nil, wait
			}
		}
		return m.newClientLocked(), 0
	}
	if m.connections > 0 && len(m.clients) < m.connections {
		return m.newClientLocked(), 0
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
		return m.newClientLocked(), 0
	}

	client := candidates[randIntn(len(candidates))]
	if client.leftUsage > 0 {
		client.leftUsage--
	}
	return client, 0
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

// normalizeXmux resolves the XMUX option set, mirroring Xray's all-or-nothing
// rule (infra/conf/transport_method.go, `if c.Xmux == (XmuxConfig{})`): the
// defaults above apply when the section is absent OR entirely empty — Xray's
// Xmux is a value field, so an empty object and a missing one are the same
// thing there, and subscription configs routinely ship the full xmux object
// with empty strings. A section with ANY field set takes every field as
// written: empty ranges stay zero (= unlimited), because per-field defaults
// would rotate connections where an Xray client would not.
func normalizeXmux(options *option.V2RayXHTTPXmuxOptions) (xmuxConfig, error) {
	if options == nil || *options == (option.V2RayXHTTPXmuxOptions{}) {
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
	config.hMaxRequestTimes, err = parseRangeOr(string(options.HMaxRequestTimes), "xmux.h_max_request_times", intRange{})
	if err != nil {
		return xmuxConfig{}, err
	}
	config.hMaxReusableSecs, err = parseRangeOr(string(options.HMaxReusableSecs), "xmux.h_max_reusable_secs", intRange{})
	if err != nil {
		return xmuxConfig{}, err
	}
	// Zero means "transport default"; negative disables keep-alive pings.
	config.keepAlivePeriod = time.Duration(options.HKeepAlivePeriod) * time.Second
	return config, nil
}
