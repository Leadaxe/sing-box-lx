//go:build with_lx_command

package daemon

import (
	"context"
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

	dialDone     chan struct{} // closed once the first-hop dial ended OK (DEPTH_CONNECT stop)
	dialDoneOnce sync.Once
	routeDone    chan struct{} // closed once the router reported the connection closed
	routeAccess  sync.Mutex
	routeClosed  bool
	routeErr     error

	httpAccess     sync.Mutex
	tlsStage       *lxtrace.Stage // running target_tls, nil once ended
	tlsErr         error
	firstByteStage *lxtrace.Stage // running first_byte, nil once ended
	httpStage      *lxtrace.Stage
	httpBegun      map[string]bool
	synthesized    map[uint32]bool
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
				return endpoint.Type()
			}
		}
		if outbound == nil {
			return ""
		}
		group, isGroup := outbound.(adapter.OutboundGroup)
		if !isGroup {
			return outbound.Type()
		}
		tag = group.Now()
	}
	return ""
}

// synthesizeAfterDial adds what the pipeline cannot report about a finished
// dial/detour stage (PLAN §2.7): a NO_ACK proxy_header child for protocols
// whose header the server never confirms, and a class attribute for the
// external modules whose QUIC and auth steps happen in one call.
func (t *connectionTest) synthesizeAfterDial(ctx context.Context, snapshot lxtrace.StageSnapshot, target string) {
	nodeType := t.nodeType(snapshot.Subject)
	if nodeType == "" {
		return
	}
	stage := t.trace().Stage(snapshot.ID)
	if stage == nil {
		return
	}
	switch nodeType {
	case C.TypeVLESS, C.TypeVMess, C.TypeTrojan, C.TypeShadowsocks:
		if snapshot.Result != lxtrace.OK {
			return
		}
		_, header := lxtrace.Begin(lxtrace.WithStage(ctx, stage), stageProxyHeader, target)
		header.Attr("protocol", nodeType)
		header.Attr("target", target)
		header.Mark(lxtrace.NoAck)
	case C.TypeHysteria2, C.TypeTUIC, C.TypeNaive:
		if snapshot.Result != lxtrace.Failed {
			return
		}
		if isAuthError(snapshot.Error) {
			stage.Attr("class", "auth")
		} else {
			stage.Attr("class", "quic")
		}
	}
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
