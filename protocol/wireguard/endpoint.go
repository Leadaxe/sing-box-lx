package wireguard

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
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
	// lx:begin idle-suspend
	// SPEC 020 idle-suspend state. lastActivity is the unix-nano timestamp of the
	// last dial through this endpoint, stamped at PostStart and on every dial entry.
	// idleAsleep is true while the endpoint is Down due to idle-suspend (distinct
	// from a deliberately-stopped endpoint, which has started=false and
	// idleAsleep=false, so it fast-paths out of resumeOnDial and is never
	// idle-woken). resumeMu serialises the idle tick's suspend decision against a
	// dial's wake.
	lastActivity atomic.Int64
	idleAsleep   atomic.Bool
	resumeMu     sync.Mutex
	// listenMode marks an endpoint with listen_port (inbound peers may connect at
	// any time). It has no dial path to wake it, so idle-suspend must skip it —
	// suspending would silently drop remote peers with no recovery.
	listenMode bool
	// torndown is true while the endpoint is at SPEC 020 level 3: asleep AND its
	// device + gVisor netstack released. It coexists with idleAsleep (a torn-down
	// endpoint is still "asleep by idle"); the difference is the wake path —
	// rebuild instead of Up. sleepSince is the unix-nano moment it fell asleep,
	// the clock lx_idle_teardown counts from (NOT the dial clock: the teardown
	// window is "how long it has been sleeping", independent of which threshold
	// put it to sleep). Both guarded by resumeMu.
	torndown   atomic.Bool
	sleepSince atomic.Int64
	// lastTransferSum is the device rx+tx total seen by the previous suspend
	// decision. Established flows never re-enter the dial path (traffic goes
	// app→conn→netstack→device), so the dial-based activity clock alone would
	// suspend an endpoint mid-transfer; any movement of this counter refreshes
	// the clock instead. Guarded by resumeMu (only the tick reads/writes it).
	lastTransferSum uint64
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
		listenMode:     options.ListenPort != 0, // lx: SPEC 020 — no dial path, never idle-suspend
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
	networkManager := service.FromContext[adapter.NetworkManager](ctx)
	var egressPool *tun.UDPEgressPool
	udpListener, isUDPListener := common.Cast[dialer.UDPListener](outboundDialer)
	if isUDPListener {
		anchorControl, egressEnabled := udpListener.UDPListenerControl()
		if egressEnabled {
			egressPool = tun.NewUDPEgressPool(tun.UDPEgressPoolOptions{
				Logger:           logger,
				Control:          anchorControl,
				InterfaceFinder:  networkManager.InterfaceFinder(),
				InterfaceMonitor: networkManager.InterfaceMonitor(),
				ExcludeInterface: options.Name,
				IsExempt: func() bool {
					return networkManager.AutoRedirectOutputMark() != 0
				},
			})
		}
	}
	wgEndpoint, err := wireguard.NewEndpoint(wireguard.EndpointOptions{
		Context:         ctx,
		Logger:          logger,
		System:          options.System,
		Handler:         ep,
		UDPTimeout:      udpTimeout,
		ICMPTimeout:     C.ICMPTimeout,
		UDPMapping:      tun.NATMapping(options.UDPMapping),
		UDPFiltering:    tun.NATFiltering(options.UDPFiltering),
		UDPNATMax:       options.UDPNATMax,
		InterfaceFinder: networkManager.InterfaceFinder(),
		EgressPool:      egressPool,
		Dialer:          outboundDialer,
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

// lx:begin idle-suspend

// idleSuspendTransferThreshold is the minimum rx+tx byte delta between two
// suspend decisions that counts as live (non-TCP) traffic. WireGuard keepalive
// (32 B / keepalive-interval) plus a periodic rekey (~240 B / ~2 min) stay far
// below it over a tick window; any real QUIC/UDP flow clears it instantly.
const idleSuspendTransferThreshold = 4096

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
// It never touches a deliberately-stopped endpoint: that one already has
// started==false but idleAsleep==false, and the `!started` check below short-
// circuits before the CAS. resumeMu mutually excludes this against resumeOnDial.
func (w *Endpoint) SuspendIfIdle(reachable bool, threshold time.Duration, reachableThreshold time.Duration) {
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.listenMode {
		// Inbound peers may connect at any time and nothing dials to wake us.
		return
	}
	effective := threshold
	if reachable {
		// Reachable endpoints stay up by default; lx_idle_suspend_reachable opts
		// them into a second, longer idle window (first dial after it pays ~1 RTT).
		if reachableThreshold <= 0 {
			return
		}
		effective = reachableThreshold
	}
	if w.IdleSince() < effective {
		return
	}
	if !w.started.Load() {
		// Already down some other way (deliberately stopped, closed).
		return
	}
	// Established flows never re-enter the dial path (app→conn→netstack→device),
	// so the dial clock alone would suspend an endpoint mid-transfer and blackhole
	// the connection. Two live-traffic gates, both off the hot path (suspend
	// candidates only):
	// 1. Established TCP flows in the device's gVisor stack — precise and immune
	//    to WireGuard keepalive/rekey noise. No activity stamp: the moment the
	//    last flow closes, the endpoint is idle again.
	if w.endpoint.ActiveTCPFlows() > 0 {
		return
	}
	// 2. Transfer-counter delta for non-TCP traffic (QUIC/UDP flows). A bare
	//    "counters moved" check would never suspend a persistent_keepalive peer
	//    (keepalive + periodic rekey move rx/tx forever), so require a real byte
	//    volume since the last decision; keepalive/rekey noise stays well under
	//    the threshold and the endpoint still suspends.
	sum := w.endpoint.TransferTotals()
	delta := sum - w.lastTransferSum // uint64 wrap on anomaly → huge delta → harmless stamp
	w.lastTransferSum = sum
	if delta >= idleSuspendTransferThreshold {
		w.stampActivity()
		return
	}
	if w.idleAsleep.CompareAndSwap(false, true) {
		w.started.Store(false)
		w.sleepSince.Store(time.Now().UnixNano()) // lx: SPEC 020 — teardown clock starts here
		w.endpoint.Suspend()                      // device.Down(): recv-workers exit, bufsArrs freed
		w.logger.Info("lx idle: suspend ", w.Tag(), " idle=", w.IdleSince().Truncate(time.Second))
	}
}

// SleepSince reports how long the endpoint has been idle-asleep, or 0 when it is
// not asleep. lx: SPEC 020 level 3 — the clock lx_idle_teardown counts from.
func (w *Endpoint) SleepSince() time.Duration {
	since := w.sleepSince.Load()
	if since == 0 || !w.idleAsleep.Load() {
		return 0
	}
	return time.Since(time.Unix(0, since))
}

// TeardownIfSlept is the idle tick's level-3 decision (SPEC 020): an endpoint
// that has been ASLEEP longer than threshold is torn down completely — device
// Closed, gVisor netstack (~5.9 MB) freed — leaving only its config. The next
// dial rebuilds it (~0.5-1 s) instead of the ~1 RTT a merely-suspended endpoint
// pays; that trade is the whole point of the level.
//
// Only ever touches an endpoint that is already idle-asleep, so every gate that
// blocked the suspend (reachability, live flows, listen-mode) has already been
// honoured: a deliberately-stopped endpoint has idleAsleep=false and is skipped
// here, and a woken endpoint clears idleAsleep under the same mutex.
func (w *Endpoint) TeardownIfSlept(threshold time.Duration) {
	if threshold <= 0 {
		return
	}
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if !w.idleAsleep.Load() || w.torndown.Load() {
		return
	}
	if w.SleepSince() < threshold {
		return
	}
	if w.torndown.CompareAndSwap(false, true) {
		slept := w.SleepSince().Truncate(time.Second)
		w.endpoint.Teardown() // device.Close(): netstack, peers, queues all freed
		w.logger.Info("lx idle: teardown ", w.Tag(), " slept=", slept)
	}
}

// resumeOnDial is called at the top of every dial entry. It stamps activity
// (always, closing the race with the idle tick) and, if the endpoint was
// idle-suspended, wakes it (device.Up()) before the dial proceeds — so the first
// write lands on a live device. Wake pays a fresh handshake (Down zeroed the
// session); that cost is on the first packet, as for any cold WG dial.
//
// Returns true if the endpoint is dialable (awake), false if it must stay down
// (deliberately stopped / closed — not an idle-suspend, so we do not resurrect it).
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
	// lx: SPEC 020 level 3 — a torn-down endpoint needs a full rebuild (new tun
	// device + netstack) and both Start stages, not just device.Up(). Concurrent
	// dials serialise on resumeMu, so only the first one rebuilds.
	if w.torndown.Load() {
		// Any failure below rolls back to a CLEAN torn-down state via Teardown()
		// (idempotent). Leaving a half-rebuilt endpoint would be worse than the
		// error itself: the tun device's events channel holds one buffered EventUp,
		// so re-running Start over a device that already sent it would block
		// forever on that channel — under resumeMu, hanging every dial.
		rebuildFailed := func(stage string, err error) bool {
			w.logger.Error("lx idle: rebuild ", w.Tag(), " ", stage, " failed: ", err)
			w.endpoint.Teardown()
			return false // flags untouched — the next dial retries from scratch
		}
		if err := w.endpoint.Rebuild(); err != nil {
			return rebuildFailed("device", err)
		}
		// Stage 1 wires the bind/device, stage 2 resolves peer domains and brings
		// the device up. Both are what a cold start runs.
		if err := w.endpoint.Start(false); err != nil {
			return rebuildFailed("start", err)
		}
		if err := w.endpoint.Start(true); err != nil {
			return rebuildFailed("post-start", err)
		}
		w.torndown.Store(false)
		w.started.Store(true)
		w.idleAsleep.Store(false)
		w.sleepSince.Store(0)
		w.logger.Info("lx idle: rebuild ", w.Tag(), " by=dial")
		return true
	}
	w.endpoint.Resume() // device.Up(): re-open socket, re-spawn recv-workers
	w.started.Store(true)
	w.idleAsleep.Store(false)
	w.sleepSince.Store(0)
	w.logger.Info("lx idle: wake ", w.Tag(), " by=dial")
	return true
}

// lx:end idle-suspend

func (w *Endpoint) Close() error {
	// lx: SPEC 020 — box.Close tears endpoints down BEFORE the router stops the
	// idle tick. Take resumeMu so an in-flight tick decision (possibly inside
	// device.Down()) finishes first, and clear both flags under it so any later
	// tick short-circuits on !started instead of calling Suspend on a closed
	// device.
	w.resumeMu.Lock()
	w.started.Store(false)
	w.idleAsleep.Store(false)
	w.torndown.Store(false) // lx: SPEC 020 level 3 — Close is idempotent over a torn-down endpoint
	w.resumeMu.Unlock()
	return w.endpoint.Close()
}

func (w *Endpoint) PreMatchFlow(network string, destination netip.Addr) adapter.PreMatchAction {
	return adapter.PreMatchFlow
}

func (w *Endpoint) PortAddresses() (netip.Addr, netip.Addr) {
	return w.endpoint.PortAddresses()
}

func (w *Endpoint) PortMTU() uint32 {
	return w.endpoint.PortMTU()
}

func (w *Endpoint) AttachReturn(returnPath tun.Return) error {
	return w.endpoint.AttachReturn(returnPath)
}

func (w *Endpoint) DetachReturn(returnPath tun.Return) error {
	return w.endpoint.DetachReturn(returnPath)
}

func (w *Endpoint) JudgeFlow(network uint8, source netip.AddrPort, destination netip.AddrPort, firstPacket []byte) tun.FlowVerdict {
	for _, localPrefix := range w.localAddresses {
		if localPrefix.Contains(destination.Addr()) {
			return tun.FlowVerdict{Action: tun.ActionAccept}
		}
	}
	return adapter.JudgeFlow(w.router, w.Tag(), w.Type(), network, source, destination, firstPacket)
}

func (w *Endpoint) WritePackets(packets [][]byte) error {
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended; L3-forward path (established flows transit here, bypassing DialContext)
		return E.New("WireGuard is not ready yet")
	}
	return w.endpoint.WritePackets(packets)
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
	if destination.IsDomain() {
		// lx: SPEC 020 — resolve BEFORE waking: the lookup does not go through this
		// endpoint (a DNS detour through it re-enters DialContext with an IP and
		// wakes it itself), so a failed lookup must not pay a full Up+handshake.
		destinationAddresses, err := w.dnsRouter.Lookup(ctx, destination.Fqdn, adapter.DNSQueryOptions{})
		if err != nil {
			return nil, err
		}
		if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
			return nil, E.New("WireGuard is not ready yet")
		}
		return N.DialSerial(ctx, w.endpoint, network, destination, destinationAddresses)
	} else if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, E.New("WireGuard is not ready yet")
	}
	return w.endpoint.DialContext(ctx, network, destination)
}

func (w *Endpoint) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	w.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	if destination.IsDomain() {
		// lx: SPEC 020 — resolve before waking (see DialContext).
		destinationAddresses, err := w.dnsRouter.Lookup(ctx, destination.Fqdn, adapter.DNSQueryOptions{})
		if err != nil {
			return nil, netip.Addr{}, err
		}
		if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
			return nil, netip.Addr{}, E.New("WireGuard is not ready yet")
		}
		return N.ListenSerial(ctx, w.endpoint, destination, destinationAddresses)
	}
	if !w.resumeOnDial() { // lx: SPEC 020 — stamp activity + wake if idle-suspended
		return nil, netip.Addr{}, E.New("WireGuard is not ready yet")
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

func (w *Endpoint) PreferredDomain(metadata *adapter.InboundContext, domain string) bool {
	return false
}

func (w *Endpoint) PreferredAddress(metadata *adapter.InboundContext, address netip.Addr) bool {
	if !w.started.Load() {
		return false
	}
	return w.endpoint.Lookup(address) != nil
}
