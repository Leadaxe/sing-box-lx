//go:build with_lx_command

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc"
)

// lx: SPEC 139 — helpers of the StartConnectionTest handler: request
// validation, source metadata, the concurrency gate, the packet pipe used by
// a UDP DEPTH_ROUTE test, and the stage synthesis the handler does on top of
// the trace the pipeline writes (proxy_header, dial classification, skipped
// placeholders).

const (
	connectionTestDefaultTimeout = 15 * time.Second
	connectionTestMaxTimeout     = 60 * time.Second
	connectionTestConcurrency    = 4

	// connectionTestInboundType marks the test connection in the connection
	// list (SPEC 139 §5.8). Rules match the inbound by tag, so the type is
	// free to carry the marker.
	connectionTestInboundType = "lx-connection-test"

	// Stage kinds the handler itself writes (SPEC 139 §4).
	stageAppDNS      = "app_dns"
	stageRoute       = "route"
	stageDial        = "dial"
	stageDetour      = "detour"
	stageProxyHeader = "proxy_header"
	stageTargetTLS   = "target_tls"
	stageFirstByte   = "first_byte"
	stageHTTP        = "http"
)

// connectionTestSlots caps the number of concurrent tests (SPEC 139 §5.10).
var connectionTestSlots = make(chan struct{}, connectionTestConcurrency)

var errConnectionTestBusy = errors.New("busy")

// connectionTest is one validated StartConnectionTest request.
type connectionTest struct {
	instance *Instance
	request  *ConnectionTestRequest

	link      *url.URL
	host      string // URL host without port
	port      uint16
	https     bool
	network   string
	depth     lxtrace.Depth
	appDNS    bool
	fresh     bool
	connectIP netip.Addr
	timeout   time.Duration
	maxBytes  int64

	// detour is set when the test goes directly through a node (outboundTag);
	// nil means through the router.
	detour    N.Dialer
	detourTag string
	router    adapter.Router
	dnsRouter adapter.DNSRouter
	metadata  adapter.InboundContext

	// run state (set by run)
	stream  grpc.ServerStreamingServer[ConnectionTestProgress]
	tr      *lxtrace.Trace
	ctx     context.Context
	cancel  context.CancelCauseFunc
	started time.Time
	target  string // destination as dialed; the proxy_header target
	conns   closers

	sendFailed atomic.Bool
	routeSeen  atomic.Bool
	pathFailed atomic.Bool // a top-level route/dial failure: later HTTP stages are skipped, not failed

	// first_byte begins once BOTH the request has been written and the
	// first-hop dial has ended OK: on the router path the request is written
	// into the pipe (and consumed by the sniffer) before the route runs, so
	// httptrace alone would start the stage before the path is up.
	requestWritten atomic.Bool
	dialFinished   atomic.Bool

	dialDone      chan struct{} // closed once the first-hop dial ended OK (DEPTH_CONNECT stop)
	dialDoneOnce  sync.Once
	routeDone     chan struct{} // closed once the router reported the connection closed
	routeDoneOnce sync.Once
	routeCalls    atomic.Int32 // RouteConnectionEx calls; the HTTP client may ask to redial (GOAWAY, REFUSED_STREAM)
	routeAccess   sync.Mutex
	routeClosed   bool
	routeErr      error

	httpAccess     sync.Mutex
	tlsStage       *lxtrace.Stage // running target_tls, nil once ended
	tlsErr         error
	firstByteStage *lxtrace.Stage // running first_byte, nil once ended
	httpStage      *lxtrace.Stage
	httpBegun      map[string]bool
	synthesized    map[uint32]bool

	multiplexOnce sync.Once
	multiplexTags map[string]bool // outbounds with multiplex enabled, from the running config
}

func (t *connectionTest) trace() *lxtrace.Trace { return t.tr }

// newConnectionTest validates the request (SPEC 139 §3.2) and resolves the
// core objects it needs. A non-nil error is the `result.error` of the stream.
func newConnectionTest(instance *Instance, request *ConnectionTestRequest) (*connectionTest, error) {
	t := &connectionTest{instance: instance, request: request}

	link, err := url.Parse(request.Link)
	if err != nil {
		return nil, E.Cause(err, "invalid link")
	}
	switch link.Scheme {
	case "http":
		t.port = 80
	case "https":
		t.port = 443
		t.https = true
	default:
		return nil, E.New("invalid link: scheme must be http or https")
	}
	if link.Hostname() == "" {
		return nil, E.New("invalid link: missing host")
	}
	if portText := link.Port(); portText != "" {
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			return nil, E.Cause(err, "invalid link port")
		}
		t.port = uint16(port)
	}
	t.link = link
	t.host = link.Hostname()

	switch request.Depth {
	case ConnectionTestDepth_DEPTH_HTTP:
		t.depth = lxtrace.DepthHTTP
	case ConnectionTestDepth_DEPTH_DNS:
		t.depth = lxtrace.DepthDNS
	case ConnectionTestDepth_DEPTH_ROUTE:
		t.depth = lxtrace.DepthRoute
	case ConnectionTestDepth_DEPTH_CONNECT:
		t.depth = lxtrace.DepthConnect
	default:
		return nil, E.New("invalid depth: ", int32(request.Depth))
	}
	// DEPTH_DNS is the app's DNS queries and nothing else, so it implies appDNS.
	t.appDNS = request.AppDNS || t.depth == lxtrace.DepthDNS
	t.fresh = request.Fresh

	switch request.Network {
	case "", N.NetworkTCP:
		t.network = N.NetworkTCP
	case N.NetworkUDP:
		if t.depth != lxtrace.DepthRoute && t.depth != lxtrace.DepthDNS {
			return nil, E.New("network udp is supported up to DEPTH_ROUTE only")
		}
		t.network = N.NetworkUDP
	default:
		return nil, E.New("invalid network: ", request.Network)
	}

	if request.ConnectIP != "" {
		addr, err := netip.ParseAddr(request.ConnectIP)
		if err != nil {
			return nil, E.Cause(err, "invalid connectIP")
		}
		t.connectIP = addr
	}

	t.timeout = connectionTestDefaultTimeout
	if request.Timeout > 0 {
		t.timeout = time.Duration(request.Timeout) * time.Millisecond
		if t.timeout > connectionTestMaxTimeout {
			t.timeout = connectionTestMaxTimeout
		}
	}
	t.maxBytes = int64(request.MaxBytes)
	if t.maxBytes <= 0 {
		t.maxBytes = getURLDefaultMaxBytes
	} else if t.maxBytes > getURLMaxBytesCeiling {
		t.maxBytes = getURLMaxBytesCeiling
	}

	metadata, err := connectionTestMetadata(instance, request.Source, t.network)
	if err != nil {
		return nil, err
	}
	t.metadata = metadata

	if request.OutboundTag != "" {
		// Both managers, as GetURLViaOutbound (058): a WG/AWG endpoint embeds
		// adapter.Outbound and dials like a plain outbound.
		tag := request.OutboundTag
		if instance.outboundManager != nil {
			if outbound, loaded := instance.outboundManager.Outbound(tag); loaded {
				t.detour = outbound
			}
		}
		if t.detour == nil && instance.endpointManager != nil {
			if endpoint, loaded := instance.endpointManager.Get(tag); loaded {
				t.detour = endpoint
			}
		}
		if t.detour == nil {
			return nil, E.New("outbound or endpoint not found: ", tag)
		}
		t.detourTag = tag
	} else {
		t.router = instance.router
		if t.router == nil && t.depth != lxtrace.DepthDNS {
			return nil, E.New("router not available")
		}
	}
	if t.appDNS {
		t.dnsRouter = service.FromContext[adapter.DNSRouter](instance.ctx)
		if t.dnsRouter == nil {
			return nil, E.New("dns router not available")
		}
	}

	return t, nil
}

// connectionTestMetadata builds the inbound metadata the test enters the
// router with (SPEC 139 §5.1, PLAN §2.3). A pre-filled ProcessInfo is not
// overwritten by the router's own process search.
func connectionTestMetadata(instance *Instance, source *ConnectionTestSource, network string) (adapter.InboundContext, error) {
	metadata := adapter.InboundContext{
		InboundType: connectionTestInboundType,
		Network:     network,
	}
	if source == nil {
		source = &ConnectionTestSource{}
	}
	if source.Inbound != "" {
		if inboundManager := service.FromContext[adapter.InboundManager](instance.ctx); inboundManager != nil {
			if _, loaded := inboundManager.Get(source.Inbound); !loaded {
				return metadata, E.New("inbound not found: ", source.Inbound)
			}
		}
		metadata.Inbound = source.Inbound
	}
	metadata.User = source.AuthUser

	sourceAddr := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if source.SourceIP != "" {
		addr, err := netip.ParseAddr(source.SourceIP)
		if err != nil {
			return metadata, E.Cause(err, "invalid sourceIP")
		}
		sourceAddr = addr
	}
	// The source port is ephemeral, as it would be for a real connection;
	// source_port rules are therefore not covered (SPEC 139 §7).
	metadata.Source = M.SocksaddrFrom(sourceAddr, uint16(49152+rand.IntN(16384)))

	if source.SourceMAC != "" {
		mac, err := net.ParseMAC(source.SourceMAC)
		if err != nil {
			return metadata, E.Cause(err, "invalid sourceMAC")
		}
		metadata.SourceMACAddress = mac
	}

	if source.PackageName != "" || source.Uid != 0 || source.ProcessPath != "" {
		owner := &adapter.ConnectionOwner{
			UserId:      -1,
			ProcessPath: source.ProcessPath,
		}
		uid := source.Uid
		if source.PackageName != "" {
			owner.AndroidPackageNames = []string{source.PackageName}
			if uid == 0 {
				// Only a platform build with a tun inbound has a package manager
				// (SPEC 139 §7); without it the uid stays 0.
				if networkManager := service.FromContext[adapter.NetworkManager](instance.ctx); networkManager != nil {
					if packageManager := networkManager.PackageManager(); packageManager != nil {
						if id, loaded := packageManager.IDByPackage(source.PackageName); loaded {
							uid = int32(id)
						}
					}
				}
			}
		}
		if uid != 0 {
			owner.UserId = uid
		}
		metadata.ProcessInfo = owner
	}
	return metadata, nil
}

// destination is the address the test connects to: connectIP, else the
// app's DNS answer, else the URL host as a domain (PLAN §2.5).
func (t *connectionTest) destination(appDNSAnswer netip.Addr) M.Socksaddr {
	if t.connectIP.IsValid() {
		return M.SocksaddrFrom(t.connectIP, t.port)
	}
	if appDNSAnswer.IsValid() {
		return M.SocksaddrFrom(appDNSAnswer, t.port)
	}
	return M.ParseSocksaddrHostPort(t.host, t.port)
}

// nodeType resolves the outbound a dial/detour stage names to the protocol
// type that actually carries the connection: selectors are followed through
// Now(); for a urltest/failover group the current pick is the best guess.
func (t *connectionTest) nodeType(tag string) string {
	_, nodeType := t.resolveNode(tag)
	return nodeType
}

// resolveNode is nodeType that also returns the tag of the resolved node.
func (t *connectionTest) resolveNode(tag string) (string, string) {
	seen := make(map[string]bool)
	for !seen[tag] {
		seen[tag] = true
		var outbound adapter.Outbound
		if t.instance.outboundManager != nil {
			if loaded, isLoaded := t.instance.outboundManager.Outbound(tag); isLoaded {
				outbound = loaded
			}
		}
		if outbound == nil && t.instance.endpointManager != nil {
			if endpoint, loaded := t.instance.endpointManager.Get(tag); loaded {
				return tag, endpoint.Type()
			}
		}
		if outbound == nil {
			return "", ""
		}
		group, isGroup := outbound.(adapter.OutboundGroup)
		if !isGroup {
			return tag, outbound.Type()
		}
		tag = adapter.GroupNow(group)
	}
	return "", ""
}

// multiplexed reports whether the outbound tag runs over sing-mux. The
// outbound does not expose it, so it is read from the running config once
// per test; without a captured config it reports false.
func (t *connectionTest) multiplexed(tag string) bool {
	t.multiplexOnce.Do(func() {
		var config struct {
			Outbounds []struct {
				Tag       string `json:"tag"`
				Multiplex *struct {
					Enabled bool `json:"enabled"`
				} `json:"multiplex"`
				Transport *struct {
					Type string `json:"type"`
				} `json:"transport"`
			} `json:"outbounds"`
		}
		if t.instance.runningConfig == "" || json.Unmarshal([]byte(t.instance.runningConfig), &config) != nil {
			return
		}
		for _, outbound := range config.Outbounds {
			// The gRPC transport keeps one grpc.ClientConn per outbound and
			// multiplexes every stream over it, as sing-mux does.
			if outbound.Multiplex != nil && outbound.Multiplex.Enabled || outbound.Transport != nil && outbound.Transport.Type == C.V2RayTransportTypeGRPC {
				if t.multiplexTags == nil {
					t.multiplexTags = make(map[string]bool)
				}
				t.multiplexTags[outbound.Tag] = true
			}
		}
	})
	return t.multiplexTags[tag]
}

// synthesizeAfterDial adds what the pipeline cannot report about a finished
// dial/detour stage (PLAN §2.7): a NO_ACK proxy_header child for protocols
// whose header the server never confirms, and a class attribute for the
// external modules whose QUIC and auth steps happen in one call. The header
// target is the hop's own destination: the test's target for the first hop,
// the address handed to a detour for a detour (its `destination` attr). The
// first hop sends its header with the first payload, so at DEPTH_CONNECT,
// where there is none, it has no header (SPEC 139 §7); a detour's header is
// part of setting the tunnel up and exists at every depth that dials. The
// stage has already ended when this runs, so attributes added to it would
// miss the snapshot on the stream: they are returned and merged into the
// outgoing snapshot by the caller.
//
// Under `fresh` a hop whose connection lives in an external module that keeps
// its own QUIC, mux or session pool (PLAN §3.3) gets reuse=uncontrolled: the
// module cannot be told to open a new connection, so the stage may have run
// over a live one.
func (t *connectionTest) synthesizeAfterDial(ctx context.Context, snapshot lxtrace.StageSnapshot, target string, firstHop bool) []lxtrace.Attr {
	nodeTag, nodeType := t.resolveNode(snapshot.Subject)
	if nodeType == "" {
		return nil
	}
	stage := t.trace().Stage(snapshot.ID)
	if stage == nil {
		return nil
	}
	var extra []lxtrace.Attr
	add := func(key, value string) {
		stage.Attr(key, value) // for Summary.Stages
		extra = append(extra, lxtrace.Attr{Key: key, Value: value})
	}
	if t.fresh && (pooledByModule(nodeType) || t.multiplexed(nodeTag)) {
		add("reuse", "uncontrolled")
	}
	switch nodeType {
	case C.TypeVLESS, C.TypeVMess, C.TypeTrojan, C.TypeShadowsocks:
		if snapshot.Result != lxtrace.OK || target == "" {
			break
		}
		if firstHop && t.depth != lxtrace.DepthHTTP {
			break
		}
		_, header := lxtrace.Begin(lxtrace.WithStage(ctx, stage), stageProxyHeader, target)
		header.Attr("protocol", nodeType)
		header.Attr("target", target)
		header.Mark(lxtrace.NoAck)
	case C.TypeHysteria2, C.TypeTUIC, C.TypeNaive:
		if snapshot.Result != lxtrace.Failed {
			break
		}
		class := "quic"
		if isAuthError(snapshot.Error) {
			class = "auth"
		}
		add("class", class)
	}
	return extra
}

// pooledByModule reports the node types whose external module reuses its own
// connections whatever the caller asks: QUIC (sing-quic: Hysteria, Hysteria2,
// TUIC), cronet's HTTP/2 and QUIC pool (naive) and the session pool of AnyTLS.
// sing-mux is per outbound and checked by multiplexed.
func pooledByModule(nodeType string) bool {
	switch nodeType {
	case C.TypeHysteria, C.TypeHysteria2, C.TypeTUIC, C.TypeNaive, C.TypeAnyTLS:
		return true
	}
	return false
}

// snapshotAttr returns the value of an attribute of a stage snapshot.
func snapshotAttr(snapshot lxtrace.StageSnapshot, key string) string {
	for _, attr := range snapshot.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}

func isAuthError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "authentication failed") || strings.Contains(lower, "status code")
}

// closers collects every connection the test opened so that the end of the
// test closes them all (SPEC 139 §5.14).
type closers struct {
	access sync.Mutex
	list   []io.Closer
}

func (c *closers) add(closer io.Closer) {
	if closer == nil {
		return
	}
	c.access.Lock()
	c.list = append(c.list, closer)
	c.access.Unlock()
}

func (c *closers) closeAll() {
	c.access.Lock()
	list := c.list
	c.list = nil
	c.access.Unlock()
	for _, closer := range list {
		closer.Close()
	}
}

// idlePacketConn is the packet pipe of a UDP DEPTH_ROUTE test: it carries no
// payload, honours read deadlines so a sniff action times out as it would on
// a silent socket, and unblocks every reader on Close.
type idlePacketConn struct {
	once     sync.Once
	done     chan struct{}
	access   sync.Mutex
	deadline chan struct{} // closed when the read deadline passes
	timer    *time.Timer
	source   M.Socksaddr
}

func newIdlePacketConn(source M.Socksaddr) *idlePacketConn {
	return &idlePacketConn{done: make(chan struct{}), source: source}
}

func (c *idlePacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	c.access.Lock()
	deadline := c.deadline
	c.access.Unlock()
	select {
	case <-c.done:
		return M.Socksaddr{}, io.ErrClosedPipe
	case <-deadline:
		return M.Socksaddr{}, os.ErrDeadlineExceeded
	}
}

func (c *idlePacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	buffer.Release()
	select {
	case <-c.done:
		return io.ErrClosedPipe
	default:
		return nil
	}
}

func (c *idlePacketConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *idlePacketConn) LocalAddr() net.Addr { return c.source.UDPAddr() }

func (c *idlePacketConn) SetDeadline(deadline time.Time) error { return c.SetReadDeadline(deadline) }

func (c *idlePacketConn) SetReadDeadline(deadline time.Time) error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if deadline.IsZero() {
		c.deadline = nil
		return nil
	}
	expired := make(chan struct{})
	c.deadline = expired
	wait := time.Until(deadline)
	if wait <= 0 {
		close(expired)
		return nil
	}
	c.timer = time.AfterFunc(wait, func() { close(expired) })
	return nil
}

func (c *idlePacketConn) SetWriteDeadline(time.Time) error { return nil }
