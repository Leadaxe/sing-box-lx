package wireguard

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var (
	_ adapter.OutboundWithPreferredRoutes = (*Endpoint)(nil)
	_ dialer.PacketDialerWithDestination  = (*Endpoint)(nil)
	_ adapter.IdleSuspendable             = (*Endpoint)(nil) // lx: SPEC 020 idle-suspend
)

func RegisterEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, NewEndpoint)
}

type Endpoint struct {
	endpoint.Adapter
	ctx            context.Context
	router         adapter.Router
	dnsRouter      adapter.DNSRouter
	logger         logger.ContextLogger
	localAddresses []netip.Prefix
	endpoint       *wireguard.Endpoint
	started        atomic.Bool
	// lx:begin awg
	// awgActive marks this endpoint as running AmneziaWG (AmneziaWGOptions.IsSet());
	// detour is its configured upstream tag. Start uses them to refuse to bring up
	// an AmneziaWG-over-WireGuard chain, which hangs the kernel on Android — see
	// awgDetourChainReachesWireGuard. The ledger lives here (not just in the dialer
	// guard) because the hang happens synchronously in Start, before any dial.
	awgActive bool
	detour    string
	// awgChainBlocked is set by Start when the AmneziaWG-over-WireGuard guard
	// fires: the device is left unstarted (started stays false) so no junk
	// handshake runs and the kernel cannot hang, while the rest of the instance
	// comes up. PostStart then skips this endpoint too.
	awgChainBlocked bool
	// lx:end awg
	// lx:begin idle-suspend
	// SPEC 020 idle-suspend state. lastActivity is the unix-nano timestamp of the
	// last dial through this endpoint, stamped at PostStart and on every dial entry.
	// idleAsleep is true while the endpoint is Down due to idle-suspend (distinct
	// from a guard-suspend, which sets started=false WITHOUT idleAsleep, so a
	// guard-suspended endpoint is never idle-woken). resumeMu serialises the idle
	// tick's suspend decision against a concurrent dial's wake.
	lastActivity atomic.Int64
	idleAsleep   atomic.Bool
	resumeMu     sync.Mutex
	// lx:end idle-suspend
}

func NewEndpoint(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardEndpointOptions) (adapter.Endpoint, error) {
	ep := &Endpoint{
		Adapter:        endpoint.NewAdapterWithDialerOptions(C.TypeWireGuard, tag, []string{N.NetworkTCP, N.NetworkUDP, N.NetworkICMP}, options.DialerOptions),
		ctx:            ctx,
		router:         router,
		dnsRouter:      service.FromContext[adapter.DNSRouter](ctx),
		logger:         logger,
		localAddresses: options.Address,
		// lx:begin awg
		awgActive: options.AmneziaWGOptions.IsSet(),
		detour:    options.Detour,
		// lx:end awg
	}
	if options.Detour != "" && options.ListenPort != 0 {
		return nil, E.New("`listen_port` is conflict with `detour`")
	}
	outboundDialer, err := dialer.NewWithOptions(dialer.Options{
		Context: ctx,
		Options: options.DialerOptions,
		RemoteIsDomain: common.Any(options.Peers, func(it option.WireGuardPeer) bool {
			return !M.ParseAddr(it.Address).IsValid()
		}),
		ResolverOnDetour: true,
	})
	if err != nil {
		return nil, err
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	wgEndpoint, err := wireguard.NewEndpoint(wireguard.EndpointOptions{
		Context:     ctx,
		Logger:      logger,
		System:      options.System,
		Handler:     ep,
		UDPTimeout:  udpTimeout,
		ICMPTimeout: C.ICMPTimeout,
		Dialer:      outboundDialer,
		CreateDialer: func(interfaceName string) N.Dialer {
			return common.Must1(dialer.NewDefault(ctx, option.DialerOptions{
				BindInterface: interfaceName,
			}))
		},
		Name:       options.Name,
		MTU:        options.MTU,
		Address:    options.Address,
		PrivateKey: options.PrivateKey,
		ListenPort: options.ListenPort,
		ResolvePeer: func(domain string) (netip.Addr, error) {
			endpointAddresses, lookupErr := ep.dnsRouter.Lookup(ctx, domain, outboundDialer.(dialer.ResolveDialer).QueryOptions())
			if lookupErr != nil {
				return netip.Addr{}, lookupErr
			}
			return endpointAddresses[0], nil
		},
		Peers: common.Map(options.Peers, func(it option.WireGuardPeer) wireguard.PeerOptions {
			return wireguard.PeerOptions{
				Endpoint:                    M.ParseSocksaddrHostPort(it.Address, it.Port),
				PublicKey:                   it.PublicKey,
				PreSharedKey:                it.PreSharedKey,
				AllowedIPs:                  it.AllowedIPs,
				PersistentKeepaliveInterval: it.PersistentKeepaliveInterval,
				Reserved:                    it.Reserved,
			}
		}),
		Workers: options.Workers,
		// lx:begin awg
		// Carry AmneziaWG 2.0 obfuscation params to the transport device. They are
		// applied only under the `with_awg` build tag; otherwise a non-empty value
		// is rejected with an explicit "awg support not built" error.
		AmneziaWG: options.AmneziaWGOptions,
		// lx:end awg
	})
	if err != nil {
		return nil, err
	}
	ep.endpoint = wgEndpoint
	return ep, nil
}

func (w *Endpoint) Start(stage adapter.StartStage) error {
	// lx:begin awg
	// Refuse to bring up an AmneziaWG endpoint whose detour chain reaches a
	// WireGuard-based endpoint: encapsulating AWG (junk handshake) inside a
	// WireGuard tunnel hangs the kernel on Android. The hang happens here, in the
	// synchronous Start path (peer-domain resolution over the detour, then the
	// device's junk handshake) — before any dial — so the lazy DetourDialer guard
	// never gets a chance to fire. We must catch it at Start instead.
	//
	// Behaviour is "variant B": do NOT return an error (that would abort the whole
	// instance start). Instead log, skip device startup, and leave started=false
	// so the rest of the config comes up and every dial through this endpoint
	// fails cleanly with "WireGuard is not ready yet". A selector/urltest in the
	// middle hides the real target at start time, so the chain walk stops at a
	// group and that case is left to the lazy DetourDialer guard at dial time.
	if stage == adapter.StartStateStart && w.awgActive && w.detour != "" {
		if outboundManager := service.FromContext[adapter.OutboundManager](w.ctx); outboundManager != nil {
			if blockedBy := awgDetourChainReachesWireGuard(outboundManager, w.detour, make(map[string]bool)); blockedBy != "" {
				w.awgChainBlocked = true
				w.logger.Error("amneziawg endpoint will not start: its detour chain reaches wireguard-based endpoint ", strconv.Quote(blockedBy), " — amneziawg over wireguard is not supported. Use a non-wireguard detour (e.g. vless).")
				return nil
			}
		}
	}
	if w.awgChainBlocked {
		return nil
	}
	// lx:end awg
	switch stage {
	case adapter.StartStateStart:
		return w.endpoint.Start(false)
	case adapter.StartStatePostStart:
		err := w.endpoint.Start(true)
		if err != nil {
			return err
		}
		w.started.Store(true)
		// lx: SPEC 020 — baseline idle clock so a never-dialed endpoint is "idle
		// since start" and only suspends after a genuine idle window, not at tick 1.
		w.stampActivity()
	}
	return nil
}

// lx:begin awg
// awgDetourChainReachesWireGuard walks the transitive detour chain starting at
// tag and returns the tag of the first WireGuard-based outbound it reaches
// (type "wireguard", covering plain WireGuard and AmneziaWG), or "" if none. It
// follows each outbound's detour dependency; it deliberately does NOT expand
// selector/urltest groups, whose chosen member is only known at runtime — that
// case is handled lazily by the DetourDialer guard. visited guards against cyclic
// detour configs. All outbounds are registered before any Start, so every tag in
// the chain is resolvable here even though some may not have started yet.
func awgDetourChainReachesWireGuard(outboundManager adapter.OutboundManager, tag string, visited map[string]bool) string {
	if tag == "" || visited[tag] {
		return ""
	}
	visited[tag] = true
	outbound, loaded := outboundManager.Outbound(tag)
	if !loaded {
		return ""
	}
	if outbound.Type() == C.TypeWireGuard {
		return tag
	}
	if _, isGroup := outbound.(adapter.OutboundGroup); isGroup {
		// Runtime-resolved target — leave it to the lazy DetourDialer guard.
		return ""
	}
	for _, dependency := range outbound.Dependencies() {
		if blockedBy := awgDetourChainReachesWireGuard(outboundManager, dependency, visited); blockedBy != "" {
			return blockedBy
		}
	}
	return ""
}

// IsAmneziaWG reports whether this endpoint runs AmneziaWG. Implements
// adapter.AmneziaWGSuspendable.
func (w *Endpoint) IsAmneziaWG() bool {
	return w.awgActive
}

// SuspendAmneziaWG brings the device down and marks the endpoint not-ready, so a
// junk handshake is never sent and every dial fails with "WireGuard is not ready
// yet". Called by the selector guard when a group this endpoint detours through
// switches to a WireGuard member (AmneziaWG over WireGuard hangs the kernel on
// Android). Idempotent. Implements adapter.AmneziaWGSuspendable.
func (w *Endpoint) SuspendAmneziaWG() {
	if w.started.CompareAndSwap(true, false) {
		w.logger.Error("amneziawg endpoint suspended: a selector in its detour chain switched to a wireguard-based member — amneziawg over wireguard is not supported")
	}
	w.endpoint.Suspend()
}

// lx:end awg

// lx:begin idle-suspend

// stampActivity records the current time as the last dial through this endpoint.
func (w *Endpoint) stampActivity() {
	w.lastActivity.Store(time.Now().UnixNano())
}

// IdleSince reports how long it has been since the last dial through this
// endpoint. A never-stamped endpoint (lastActivity == 0) reports a very large
// duration; PostStart stamps a baseline so this never happens for a live one.
func (w *Endpoint) IdleSince() time.Duration {
	last := w.lastActivity.Load()
	if last == 0 {
		return time.Duration(1<<63 - 1)
	}
	return time.Since(time.Unix(0, last))
}

// SuspendIfIdle is the idle tick's per-endpoint decision (SPEC 020). It brings
// the endpoint Down — freeing the recv-worker bufsArrs, the dominant GC-scan
// holder — when it is unreachable from the active routing tree AND has been idle
// past the threshold. Silent on every non-transition (edge-triggered logging).
//
// It never touches a guard-suspended endpoint: that one already has
// started==false but idleAsleep==false, and the `!started` guard below short-
// circuits before the CAS. resumeMu mutually excludes this against resumeOnDial.
func (w *Endpoint) SuspendIfIdle(reachable bool, threshold time.Duration) {
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if reachable || w.IdleSince() < threshold {
		return
	}
	if !w.started.Load() {
		// Already down some other way (guard-suspend, awg-chain-blocked, closed).
		return
	}
	if w.idleAsleep.CompareAndSwap(false, true) {
		w.started.Store(false)
		w.endpoint.Suspend() // device.Down(): recv-workers exit, bufsArrs freed
		w.logger.Info("lx idle: suspend ", w.Tag(), " idle=", w.IdleSince().Truncate(time.Second))
	}
}

// resumeOnDial is called at the top of every dial entry. It stamps activity
// (always, closing the race with the idle tick) and, if the endpoint was
// idle-suspended, wakes it (device.Up()) before the dial proceeds — so the first
// write lands on a live device. Wake pays a fresh handshake (Down zeroed the
// session); that cost is on the first packet, as for any cold WG dial.
//
// Returns true if the endpoint is dialable (awake), false if it must stay down
// (guard-suspend / chain-blocked — not an idle-suspend, so we do not resurrect it).
func (w *Endpoint) resumeOnDial() bool {
	w.stampActivity()
	if !w.idleAsleep.Load() {
		// Fast path: either fully awake, or down for a non-idle reason we must not wake.
		return w.started.Load()
	}
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if !w.idleAsleep.Load() {
		return w.started.Load()
	}
	w.endpoint.Resume() // device.Up(): re-open socket, re-spawn recv-workers
	w.started.Store(true)
	w.idleAsleep.Store(false)
	w.logger.Info("lx idle: wake ", w.Tag(), " by=dial")
	return true
}

// lx:end idle-suspend

func (w *Endpoint) Close() error {
	w.started.Store(false)
	return w.endpoint.Close()
}

func (w *Endpoint) PrepareConnection(network string, source M.Socksaddr, destination M.Socksaddr, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, E.New("WireGuard is not ready yet")
	}
	var ipVersion uint8
	if !destination.IsIPv6() {
		ipVersion = 4
	} else {
		ipVersion = 6
	}
	routeDestination, err := w.router.PreMatch(adapter.InboundContext{
		Inbound:     w.Tag(),
		InboundType: w.Type(),
		IPVersion:   ipVersion,
		Network:     network,
		Source:      source,
		Destination: destination,
	}, routeContext, timeout, false)
	if err != nil {
		switch {
		case rule.IsBypassed(err):
			err = nil
		case rule.IsRejected(err):
			w.logger.Trace("reject ", network, " connection from ", source.AddrString(), " to ", destination.AddrString())
		default:
			if network == N.NetworkICMP {
				w.logger.Warn(E.Cause(err, "link ", network, " connection from ", source.AddrString(), " to ", destination.AddrString()))
			}
		}
	}
	return routeDestination, err
}

func (w *Endpoint) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = w.Tag()
	metadata.InboundType = w.Type()
	metadata.Source = source
	for _, localPrefix := range w.localAddresses {
		if localPrefix.Contains(destination.Addr) {
			metadata.OriginDestination = destination
			if destination.Addr.Is4() {
				destination.Addr = netip.AddrFrom4([4]uint8{127, 0, 0, 1})
			} else {
				destination.Addr = netip.IPv6Loopback()
			}
			break
		}
	}
	metadata.Destination = destination
	w.logger.InfoContext(ctx, "inbound connection from ", source)
	w.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	w.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (w *Endpoint) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = w.Tag()
	metadata.InboundType = w.Type()
	metadata.Source = source
	metadata.Destination = destination
	for _, localPrefix := range w.localAddresses {
		if localPrefix.Contains(destination.Addr) {
			metadata.OriginDestination = destination
			if destination.Addr.Is4() {
				metadata.Destination.Addr = netip.AddrFrom4([4]uint8{127, 0, 0, 1})
			} else {
				metadata.Destination.Addr = netip.IPv6Loopback()
			}
			conn = bufio.NewNATPacketConn(bufio.NewNetPacketConn(conn), metadata.OriginDestination, metadata.Destination)
		}
	}
	w.logger.InfoContext(ctx, "inbound packet connection from ", source)
	w.logger.InfoContext(ctx, "inbound packet connection to ", destination)
	w.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (w *Endpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch network {
	case N.NetworkTCP:
		w.logger.InfoContext(ctx, "outbound connection to ", destination)
	case N.NetworkUDP:
		w.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	}
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, E.New("WireGuard is not ready yet")
	}
	if destination.IsDomain() {
		destinationAddresses, err := w.dnsRouter.Lookup(ctx, destination.Fqdn, adapter.DNSQueryOptions{})
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, w.endpoint, network, destination, destinationAddresses)
	} else if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return w.endpoint.DialContext(ctx, network, destination)
}

func (w *Endpoint) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	w.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, netip.Addr{}, E.New("WireGuard is not ready yet")
	}
	if destination.IsDomain() {
		destinationAddresses, err := w.dnsRouter.Lookup(ctx, destination.Fqdn, adapter.DNSQueryOptions{})
		if err != nil {
			return nil, netip.Addr{}, err
		}
		return N.ListenSerial(ctx, w.endpoint, destination, destinationAddresses)
	}
	packetConn, err := w.endpoint.ListenPacket(ctx, destination)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	if destination.IsIP() {
		return packetConn, destination.Addr, nil
	}
	return packetConn, netip.Addr{}, nil
}

func (w *Endpoint) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	packetConn, destinationAddress, err := w.ListenPacketWithDestination(ctx, destination)
	if err != nil {
		return nil, err
	}
	if destinationAddress.IsValid() && destination != M.SocksaddrFrom(destinationAddress, destination.Port) {
		return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
	}
	return packetConn, nil
}

func (w *Endpoint) PreferredDomain(domain string) bool {
	return false
}

func (w *Endpoint) PreferredAddress(address netip.Addr) bool {
	if !w.started.Load() {
		return false
	}
	return w.endpoint.Lookup(address) != nil
}

func (w *Endpoint) NewDirectRouteConnection(metadata adapter.InboundContext, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, E.New("WireGuard is not ready yet")
	}
	return w.endpoint.NewDirectRouteConnection(metadata, routeContext, timeout)
}
