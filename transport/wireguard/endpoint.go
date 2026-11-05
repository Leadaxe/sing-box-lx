package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/lxdatagram"
	"github.com/sagernet/sing-box/service/powerreport"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
	"github.com/sagernet/wireguard-go/conn"
	"github.com/sagernet/wireguard-go/device"

	"go4.org/netipx"
)

type Endpoint struct {
	options        EndpointOptions
	peers          []peerConfig
	ipcConf        string
	allowedAddress []netip.Prefix
	tunDevice      Device
	returnDevice   *returnDeviceWrapper
	device         atomic.Pointer[device.Device]
	allowedIPs     *device.AllowedIPs
	egressPool     *tun.UDPEgressPool
	pause          pause.Manager
	pauseCallback  *list.Element[pause.Callback]
	stateAccess    sync.Mutex
	suspended      atomic.Bool
	networkPaused  bool
	// lx: SPEC 020 level 3 — the recipe for rebuilding the tun device after a
	// Teardown released it (Device/stack objects are one-shot), or that a lazy
	// endpoint (SPEC 097) builds on first dial. inet4/inet6 are the port
	// addresses cached from the device so PortAddresses() never has to touch a
	// possibly-nil tunDevice.
	deviceOptions DeviceOptions
	inet4Address  netip.Addr
	inet6Address  netip.Addr
	// lx: SPEC 020 — test seam standing in for a device.Up() failure, which needs
	// a real bind to reproduce. Nil in production.
	resumeErrHook func() error
	// lx: SPEC 145 — session start for the per-peer status (peer_session_lx.go).
	session peerSession
	// lx: SPEC 030 — pre-passes requested (endpoint_quiesce_lx.go); tests only.
	quiesceCount atomic.Uint32
}

// SetResumeErrHookForTest injects a Resume failure. Test-only.
func (e *Endpoint) SetResumeErrHookForTest(hook func() error) {
	e.resumeErrHook = hook
}

func NewEndpoint(options EndpointOptions) (*Endpoint, error) {
	if options.PrivateKey == "" {
		return nil, E.New("missing private key")
	}
	privateKeyBytes, err := base64.StdEncoding.DecodeString(options.PrivateKey)
	if err != nil {
		return nil, E.Cause(err, "decode private key")
	}
	privateKey := hex.EncodeToString(privateKeyBytes)
	ipcConf := "private_key=" + privateKey
	if options.ListenPort != 0 {
		ipcConf += "\nlisten_port=" + F.ToString(options.ListenPort)
	}
	// lx:begin awg
	// Append AmneziaWG 2.0 device-global obfuscation lines (jc=/h1=/i1=…) to the
	// device IpcSet config. awgIpcLines is tag-gated: with `with_awg` it formats
	// the params; without it, a non-empty AWG config returns an explicit error.
	awgLines, err := awgIpcLines(options.AmneziaWG)
	if err != nil {
		return nil, err
	}
	ipcConf += awgLines
	// lx:end awg
	var peers []peerConfig
	for peerIndex, rawPeer := range options.Peers {
		// lx: awg — the keepalive is a spec string ("N" / "min-max" / "");
		// the range form is AWG 3.x and tag-gated like the other AWG fields.
		keepalive, err := awgKeepaliveSpec(rawPeer.PersistentKeepaliveInterval)
		if err != nil {
			return nil, E.Cause(err, "persistent_keepalive_interval for peer ", peerIndex)
		}
		peer := peerConfig{
			allowedIPs: rawPeer.AllowedIPs,
			keepalive:  keepalive,
		}
		if rawPeer.Endpoint.Addr.IsValid() {
			peer.endpoint = rawPeer.Endpoint.AddrPort()
		} else if rawPeer.Endpoint.IsDomain() {
			peer.destination = rawPeer.Endpoint
		}
		publicKeyBytes, err := base64.StdEncoding.DecodeString(rawPeer.PublicKey)
		if err != nil {
			return nil, E.Cause(err, "decode public key for peer ", peerIndex)
		}
		if len(publicKeyBytes) != device.NoisePublicKeySize {
			return nil, E.New("invalid public key for peer ", peerIndex, ", required ", device.NoisePublicKeySize, " bytes, got ", len(publicKeyBytes))
		}
		peer.publicKey = device.NoisePublicKey(publicKeyBytes)
		if rawPeer.PreSharedKey != "" {
			preSharedKeyBytes, err := base64.StdEncoding.DecodeString(rawPeer.PreSharedKey)
			if err != nil {
				return nil, E.Cause(err, "decode pre shared key for peer ", peerIndex)
			}
			peer.preSharedKeyHex = hex.EncodeToString(preSharedKeyBytes)
		}
		if len(rawPeer.AllowedIPs) == 0 {
			return nil, E.New("missing allowed ips for peer ", peerIndex)
		}
		if len(rawPeer.Reserved) > 0 {
			if len(rawPeer.Reserved) != 3 {
				return nil, E.New("invalid reserved value for peer ", peerIndex, ", required 3 bytes, got ", len(rawPeer.Reserved))
			}
			copy(peer.reserved[:], rawPeer.Reserved[:])
		}
		peers = append(peers, peer)
	}
	var allowedPrefixBuilder netipx.IPSetBuilder
	for _, peer := range options.Peers {
		for _, prefix := range peer.AllowedIPs {
			allowedPrefixBuilder.AddPrefix(prefix)
		}
	}
	allowedIPSet, err := allowedPrefixBuilder.IPSet()
	if err != nil {
		return nil, err
	}
	allowedAddresses := allowedIPSet.Prefixes()
	// lx:begin awg
	// AmneziaWG's s4 prepends junk bytes to every transport (data) message, so an
	// AWG endpoint needs a lower MTU than plain WireGuard (see docs-lx/lx-config.md
	// §2). s3 pads only cookie-reply messages (device.paddings.cookie), never data
	// packets, so it is NOT part of the per-transport overhead (SPEC 022 #8).
	// awgJunk = the per-transport-packet overhead = s4.
	awgJunk := options.AmneziaWG.S4
	// lx:end awg
	if options.MTU == 0 {
		options.MTU = 1408
		// lx: when AWG junk is active the plain 1408 default already overflows the
		// path, so fall back to the recommended AWG client MTU instead of shipping
		// (and then warning about) a value we picked ourselves.
		if awgJunk > 0 {
			options.MTU = 1280
		}
	}
	// lx:begin awg
	// Warn when an explicitly-set MTU is still too high for the junk overhead: the
	// handshake succeeds but transport packets fail with EMSGSIZE ("sendmsg:
	// message too long"). The path budget assumes a conservative 1492-byte (PPPoE)
	// path; raw Ethernet is 1500.
	if awgJunk > 0 {
		const pathMTU = 1492
		const wgOverhead = 28 + 32 // UDP/IP + WireGuard transport header
		if budget := pathMTU - wgOverhead - int(awgJunk); int(options.MTU) > budget {
			options.Logger.Warn(fmt.Sprintf(
				"amneziawg: mtu %d may be too high for s4 junk (%d bytes); "+
					"transport packets can exceed a %d-byte path and fail with "+
					"\"message too long\". Consider mtu <= %d (or 1280). See docs-lx/lx-config.md",
				options.MTU, awgJunk, pathMTU, budget,
			))
		}
	}
	// lx:end awg
	return &Endpoint{
		options:        options,
		peers:          peers,
		ipcConf:        ipcConf,
		allowedAddress: allowedAddresses,
	}, nil
}

func (e *Endpoint) Initialize(memoryPressure func() tun.MemoryPressure) error {
	options := e.options
	deviceOptions := DeviceOptions{
		Context:         options.Context,
		Logger:          options.Logger,
		System:          options.System,
		Handler:         options.Handler,
		UDPTimeout:      options.UDPTimeout,
		ICMPTimeout:     options.ICMPTimeout,
		UDPMapping:      options.UDPMapping,
		UDPFiltering:    options.UDPFiltering,
		UDPNATMax:       options.UDPNATMax,
		InterfaceFinder: options.InterfaceFinder,
		MemoryPressure:  memoryPressure,
		CreateDialer:    options.CreateDialer,
		Name:            options.Name,
		MTU:             options.MTU,
		Address:         options.Address,
		AllowedAddress:  e.allowedAddress,
	}
	// lx: SPEC 020 level 3 / SPEC 097 — keep the recipe so a torn-down or lazy
	// endpoint can build the device later; the port addresses are static config.
	e.deviceOptions = deviceOptions
	e.inet4Address, e.inet6Address = devicePortAddresses(deviceOptions)
	// lx:begin lazy-build
	if options.LazyDevice {
		e.initializeLazy()
		return nil
	}
	// lx:end lazy-build
	tunDevice, err := NewDevice(deviceOptions)
	if err != nil {
		return E.Cause(err, "create WireGuard device")
	}
	e.tunDevice = tunDevice
	e.returnDevice = &returnDeviceWrapper{Device: tunDevice}
	return nil
}

// lx:begin idle-suspend
// Teardown releases EVERYTHING this endpoint holds — the wireguard device AND
// the tun device with its network stack — while keeping the recipe
// (options/peers/ipcConf) so Rebuild can bring it back. SPEC 020 level 3: the
// tick calls it for an endpoint that has been asleep past lx_idle_teardown.
// Idempotent; the endpoint stays flagged suspended (only a dial rebuilds it).
func (e *Endpoint) Teardown() {
	e.suspended.Store(true)
	// Release the device under stateAccess, the mutex the pause handler and
	// SetIdle take, so neither can touch a device that is being shut down.
	e.stateAccess.Lock()
	if wgDevice := e.device.Swap(nil); wgDevice != nil {
		wgDevice.Down()
		wgDevice.Close()
	}
	e.resetSession() // lx: SPEC 145 — a rebuilt device counts from zero
	e.stateAccess.Unlock()
	e.closeTunDevice()
	e.allowedIPs = nil
	if e.pauseCallback != nil {
		e.pause.UnregisterCallback(e.pauseCallback)
		e.pauseCallback = nil
	}
}

// Rebuild recreates the tun device (and its netstack) after a Teardown, leaving
// the caller to run Start again. Peers keep whatever endpoints were resolved
// before the teardown, so a rebuild does not re-resolve unless a peer is
// domain-based and was never resolved. lx: SPEC 020 level 3.
func (e *Endpoint) Rebuild() error {
	if e.tunDevice != nil {
		return nil // never torn down (or already rebuilt)
	}
	tunDevice, err := newDeviceFn(e.deviceOptions) // lx: SPEC 097 — counted in tests
	if err != nil {
		return E.Cause(err, "rebuild WireGuard device")
	}
	e.tunDevice = tunDevice
	// Carry the attached L3 return path over to the fresh wrapper: sing-tun
	// attached it once (AttachReturn) and will not re-attach after a rebuild it
	// knows nothing about — dropping it would silently break the L3 downlink.
	returnDevice := &returnDeviceWrapper{Device: tunDevice}
	if previous := e.returnDevice.state.Load(); previous != nil {
		returnDevice.state.Store(previous)
	}
	e.returnDevice = returnDevice
	e.inet4Address = tunDevice.Inet4Address()
	e.inet6Address = tunDevice.Inet6Address()
	e.suspended.Store(false)
	return nil
}

// TornDown reports whether the tun device is currently released (level 3).
func (e *Endpoint) TornDown() bool {
	return e.tunDevice == nil
}

// lx:end idle-suspend

func (e *Endpoint) Start(postStart bool) error {
	hasDomainPeer := common.Any(e.peers, func(peer peerConfig) bool {
		return peer.destination.IsDomain()
	})
	if postStart != hasDomainPeer {
		return nil
	}
	var bind conn.Bind
	// lx: SPEC 153 — how the masquerade burst learns which path it is on.
	var pathIsKernel func() (bool, error)
	udpListener, isUDPListener := common.Cast[dialer.UDPListener](e.options.Dialer)
	if isUDPListener {
		listenerControl, egressEnabled := udpListener.UDPListenerControl()
		standardBind := conn.NewStdNetBind(listenerControl).(*conn.StdNetBind)
		if e.options.ListenPort == 0 && len(e.peers) == 1 && e.peers[0].endpoint.IsValid() {
			standardBind.SetSinglePeerMode()
		}
		if egressEnabled {
			egressPoolOptions := e.options.EgressPoolOptions
			egressPoolOptions.Control = listenerControl
			e.egressPool = tun.NewUDPEgressPool(egressPoolOptions)
			standardBind.SetEgressProvider(e.egressPool)
		}
		powerManager := service.FromContext[*powerreport.Manager](e.options.Context)
		if powerManager != nil {
			recorder := powerManager.Recorder()
			if recorder != nil {
				attribution := &powerreport.Attribution{Endpoint: e.options.Tag}
				counter := recorder.TrafficCounter(powerreport.TrafficEndpoint, e.options.Tag)
				standardBind.SetIOActivityFuncs(func(size int) {
					counter.CountIn(int64(size))
					recorder.Touch(powerreport.DirectionInbound, size, attribution)
				}, func(size int) {
					counter.CountOut(int64(size))
					recorder.Touch(powerreport.DirectionOutbound, size, attribution)
				})
			}
		}
		bind = standardBind
		pathIsKernel = func() (bool, error) { return true, nil }
	} else {
		var (
			isConnect   bool
			connectAddr netip.AddrPort
			reserved    [3]uint8
		)
		if len(e.peers) == 1 {
			reserved = e.peers[0].reserved
			if e.peers[0].endpoint.IsValid() {
				isConnect = true
				connectAddr = e.peers[0].endpoint
			}
		}
		clientBind := NewClientBind(e.options.Context, e.options.Logger, e.options.Dialer, isConnect, connectAddr, reserved)
		bind = clientBind
		pathIsKernel = clientBind.PathIsKernel
	}
	if isUDPListener || len(e.peers) > 1 {
		for _, peer := range e.peers {
			if peer.endpoint.IsValid() && peer.reserved != [3]uint8{} {
				bind.SetReservedForEndpoint(peer.endpoint, peer.reserved)
			}
		}
	}
	err := e.tunDevice.Start()
	if err != nil {
		return err
	}
	logger := &device.Logger{
		Verbosef: func(format string, args ...any) {
			e.options.Logger.Debug(fmt.Sprintf(strings.ToLower(format), args...))
		},
		Errorf: func(format string, args ...any) {
			e.options.Logger.Error(fmt.Sprintf(strings.ToLower(format), args...))
		},
	}
	wgDevice := device.NewDevice(e.options.Context, e.returnDevice, bind, logger, e.options.Workers)
	// lx: SPEC 041 — passive self-heal: on handshake give-up the device rebinds
	// its socket (fresh ephemeral port unless the user pinned listen_port) and
	// re-initiates, so a dead NAT/DPI flow entry cannot hold the endpoint in
	// ERR until a manual reconnect.
	wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)
	// lx: SPEC 009 — the ip=quic masquerade decoy is generated per handshake
	// (fresh DCID/ciphertext each time) instead of a static i1 CPS blob.
	if decoy := awgDecoyFunc(e.options.AmneziaWG, e.options.Logger); decoy != nil {
		wgDevice.SetDecoyPacketsFunc(decoy)
	}
	// lx: SPEC 153 — the masquerade burst goes out by path (lx.awg_masquerade)
	// and, when QUIC-shaped, behind the lx.quic_sniff_decoy byte.
	if e.options.AmneziaWG.IsSet() {
		if filter := newHandshakeBurstFilter(lxdatagram.AWGMasqueradeFromContext(e.options.Context), lxdatagram.SniffDecoyFromContext(e.options.Context), pathIsKernel, e.options.Logger); filter != nil {
			wgDevice.SetHandshakeBurstFunc(filter)
		}
	}
	domainPeers := make(map[device.NoisePublicKey]*peerConfig)
	for peerIndex, peer := range e.peers {
		if peer.destination.IsDomain() {
			domainPeers[peer.publicKey] = &e.peers[peerIndex]
		}
	}
	if len(domainPeers) > 0 {
		wgDevice.SetEndpointResolverFunc(func(publicKey device.NoisePublicKey) ([]conn.Endpoint, error) {
			peer, found := domainPeers[publicKey]
			if !found {
				return nil, nil
			}
			addresses, lookupErr := e.options.ResolvePeer(peer.destination.Fqdn)
			if lookupErr != nil {
				return nil, lookupErr
			}
			endpoints := make([]conn.Endpoint, 0, len(addresses))
			for _, address := range addresses {
				destination := netip.AddrPortFrom(address, peer.destination.Port)
				if peer.reserved != ([3]uint8{}) {
					bind.SetReservedForEndpoint(destination, peer.reserved)
				}
				endpoint, parseErr := bind.ParseEndpoint(destination.String())
				if parseErr != nil {
					return nil, parseErr
				}
				endpoints = append(endpoints, endpoint)
			}
			return endpoints, nil
		})
	}
	var ipcConf strings.Builder
	ipcConf.WriteString(e.ipcConf)
	for _, peer := range e.peers {
		ipcConf.WriteString(peer.GenerateIpcLines())
	}
	err = wgDevice.IpcSet(ipcConf.String())
	if err != nil {
		wgDevice.Close()
		return E.Cause(err, "setup wireguard: \n", ipcConf.String())
	}
	wgPeers := make([]*device.Peer, 0, len(e.peers))
	for _, peer := range e.peers {
		wgPeer, loaded := wgDevice.LookupActivePeer(peer.publicKey)
		if loaded {
			wgPeers = append(wgPeers, wgPeer)
		}
	}
	e.tunDevice.SetDevice(wgDevice, wgPeers)
	e.device.Store(wgDevice)
	e.pause = service.FromContext[pause.Manager](e.options.Context)
	if e.pause != nil {
		e.pauseCallback = e.pause.RegisterCallback(e.onPauseUpdated)
	}
	e.allowedIPs = wgDevice.AllowedIPs()
	return nil
}

func (e *Endpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if !destination.Addr.IsValid() {
		return nil, E.Cause(os.ErrInvalid, "invalid non-IP destination")
	}
	// lx: SPEC 020 level 3 — a torn-down endpoint has no tun device; the protocol
	// layer rebuilds before dialing, so reaching here means a caller bypassed it.
	if e.tunDevice == nil {
		return nil, E.New("WireGuard endpoint is torn down")
	}
	e.resume()
	return e.tunDevice.DialContext(ctx, network, destination)
}

func (e *Endpoint) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if !destination.Addr.IsValid() {
		return nil, E.Cause(os.ErrInvalid, "invalid non-IP destination")
	}
	if e.tunDevice == nil { // lx: SPEC 020 level 3
		return nil, E.New("WireGuard endpoint is torn down")
	}
	e.resume()
	return e.tunDevice.ListenPacket(ctx, destination)
}

func (e *Endpoint) SetIdle(idle bool) {
	e.stateAccess.Lock()
	defer e.stateAccess.Unlock()
	wgDevice := e.device.Load()
	if wgDevice == nil {
		return
	}
	if idle {
		if e.suspended.Load() {
			return
		}
		e.suspended.Store(true)
		wgDevice.Down()
	} else {
		e.resumeLocked(wgDevice)
	}
}

func (e *Endpoint) resume() {
	if !e.suspended.Load() {
		return
	}
	e.stateAccess.Lock()
	defer e.stateAccess.Unlock()
	wgDevice := e.device.Load()
	if wgDevice == nil {
		return
	}
	e.resumeLocked(wgDevice)
}

func (e *Endpoint) resumeLocked(wgDevice *device.Device) {
	if !e.suspended.Load() {
		return
	}
	e.suspended.Store(false)
	if !e.networkPaused {
		wgDevice.Up()
	}
}

func (e *Endpoint) Close() error {
	if e.pauseCallback != nil {
		e.pause.UnregisterCallback(e.pauseCallback)
		e.pauseCallback = nil
	}
	if e.egressPool != nil {
		e.egressPool.Close()
		e.egressPool = nil
	}
	e.stateAccess.Lock()
	wgDevice := e.device.Swap(nil)
	if wgDevice != nil {
		wgDevice.Down()
		wgDevice.Close()
	}
	e.stateAccess.Unlock()
	if wgDevice != nil {
		return nil
	}
	return e.closeTunDevice() // lx: SPEC 020 level 3 — see endpoint_close_lx.go
}

// lx:begin awg
// Suspend brings the WireGuard device down without closing it, stopping the
// (junk) handshake machinery. Used by the selector guard to neutralise an
// AmneziaWG endpoint when a group it detours through switches to a WireGuard
// member. Idempotent — a nil device (never started / already closed) is a no-op.
//
// SPEC 020 reuses Suspend for idle-suspend: device.Down() closes the UDP socket,
// which makes RoutineReceiveIncoming exit and release its bufsArrs (the dominant
// per-endpoint heap / GC-scan holder, ~8 MB per recv-worker). The trade-off is
// that Down zeroes the crypto session, so Resume pays a fresh handshake.
func (e *Endpoint) Suspend() {
	e.stateAccess.Lock()
	defer e.stateAccess.Unlock()
	e.suspended.Store(true)
	if wgDevice := e.device.Load(); wgDevice != nil {
		wgDevice.Down()
		e.markSessionDown() // lx: SPEC 145
	}
}

// Resume brings a Suspend'd device back up (device.Up()): re-opens the UDP
// socket, re-spawns the recv-workers (re-allocating bufsArrs), and initiates a
// fresh handshake on the next packet. Idempotent and nil-safe. Used by SPEC 020
// idle-suspend to wake an endpoint lazily on the next dial through it.
//
// The error is returned, not swallowed: device.Up() runs BindUpdate as its first
// step, so it fails whenever the UDP socket cannot be re-opened (a network
// transition racing the wake, a protect callback refusing the fd). wireguard-go
// then falls back to deviceStateDown, and a caller that assumed success would
// leave the endpoint marked live over a device that is down — silently
// black-holing every packet routed through it, with no path back up.
func (e *Endpoint) Resume() error {
	e.stateAccess.Lock()
	defer e.stateAccess.Unlock()
	e.suspended.Store(false)
	if e.resumeErrHook != nil { // tests only: stand in for a device.Up() failure
		return e.resumeErrHook()
	}
	wgDevice := e.device.Load()
	if wgDevice == nil || e.networkPaused {
		return nil
	}
	e.markSessionUp() // lx: SPEC 145
	return wgDevice.Up()
}

// ActiveTCPFlows reports the number of ESTABLISHED TCP connections inside the
// device's network stack (0 for the system-interface device, which has no
// stack, and 0 while the stack does not expose the counter). lx: SPEC 020 — precise, keepalive-immune "live flows" gate for the
// idle tick.
func (e *Endpoint) ActiveTCPFlows() uint64 {
	if counter, ok := e.tunDevice.(interface{ CurrentEstablished() uint64 }); ok {
		return counter.CurrentEstablished()
	}
	return 0
}

// TransferTotals reports the sum of rx+tx bytes across all peers, read from the
// device's IPC state. The SPEC 020 idle tick compares it between ticks to detect
// traffic on established flows, which never re-enters the protocol dial path.
// NOTE: WireGuard keepalive and rekey traffic moves these counters too — the
// caller must apply a byte threshold, not a bare "changed" check, or a
// persistent_keepalive peer would never be considered idle. Only called for
// suspend candidates (unreachable + dial-idle), so the IpcGet string round-trip
// is off the hot path. Returns 0 for a nil device.
func (e *Endpoint) TransferTotals() uint64 {
	dev := e.device.Load()
	if dev == nil {
		return 0
	}
	ipc, err := dev.IpcGet()
	if err != nil {
		return 0
	}
	var total uint64
	for _, line := range strings.Split(ipc, "\n") {
		if value, ok := strings.CutPrefix(line, "rx_bytes="); ok {
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				total += n
			}
		} else if value, ok := strings.CutPrefix(line, "tx_bytes="); ok {
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				total += n
			}
		}
	}
	return total
}

// lx:end awg

func (e *Endpoint) Lookup(address netip.Addr) *device.Peer {
	if e.allowedIPs == nil {
		return nil
	}
	return e.allowedIPs.LookupFromPacket(netip.Addr{}, address, nil)
}

func (e *Endpoint) BindUpdate() error {
	wgDevice := e.device.Load()
	if wgDevice == nil {
		return nil
	}
	return wgDevice.BindUpdate()
}

// lx: SPEC 041 v2 — wake-nudge passthrough: rebind the device's socket now if
// its session is provably dead (see device.RebindIfSessionStale). Nil-safe
// over a torn-down endpoint (SPEC 020 level 3 releases the device).
func (e *Endpoint) RebindIfSessionStale() bool {
	wgDevice := e.device.Load()
	if wgDevice == nil {
		return false
	}
	return wgDevice.RebindIfSessionStale()
}

func (e *Endpoint) onPauseUpdated(event int) {
	switch event {
	case pause.EventNetworkPause, pause.EventNetworkWake:
		go e.updateNetworkPause()
	}
}

func (e *Endpoint) updateNetworkPause() {
	e.stateAccess.Lock()
	defer e.stateAccess.Unlock()
	wgDevice := e.device.Load()
	if wgDevice == nil {
		return
	}
	e.networkPaused = e.pause.IsNetworkPaused()
	if e.networkPaused {
		wgDevice.Down()
	} else if !e.suspended.Load() {
		wgDevice.Up()
	}
}

type peerConfig struct {
	destination     M.Socksaddr
	endpoint        netip.AddrPort
	publicKey       device.NoisePublicKey
	preSharedKeyHex string
	allowedIPs      []netip.Prefix
	keepalive       string // lx: awg — canonical spec, "" = off
	reserved        [3]uint8
}

func (c peerConfig) GenerateIpcLines() string {
	var ipcLines strings.Builder
	ipcLines.WriteString("\npublic_key=" + hex.EncodeToString(c.publicKey[:]))
	if c.endpoint.IsValid() {
		ipcLines.WriteString("\nendpoint=" + c.endpoint.String())
	}
	if c.preSharedKeyHex != "" {
		ipcLines.WriteString("\npreshared_key=" + c.preSharedKeyHex)
	}
	for _, allowedIP := range c.allowedIPs {
		ipcLines.WriteString("\nallowed_ip=" + allowedIP.String())
	}
	if c.keepalive != "" {
		ipcLines.WriteString("\npersistent_keepalive_interval=" + c.keepalive)
	}
	return ipcLines.String()
}
