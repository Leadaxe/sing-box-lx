// Package masque implements the MASQUE (CONNECT-IP / RFC 9484) adapter.Outbound
// — the outbound-registry layer over transport/masque, primarily targeting
// Cloudflare WARP. It builds a userspace gVisor stack per tunnel and pumps IP
// packets between it and the CONNECT-IP tunnel (h3 or h2). See SPEC 021.
//
// Not to be confused with transport/wireguard/masque_awg.go (SPEC 009 AmneziaWG
// "masquerade" obfuscation) — unrelated despite the name.
package masque

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/masque"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.MASQUEOutboundOptions](registry, C.TypeMASQUE, NewOutbound)
}

const defaultMTU uint32 = 1280

// defaultIdleTimeout is how long a tunnel stays up with no traffic before it is
// suspended (torn down) to free the userspace stack, pumps and QUIC keepalive.
const defaultIdleTimeout = 5 * time.Minute

// maxH2MTU caps the userspace MTU on the h2 transport: one IP packet becomes one
// HTTP/2 DATA frame, which must fit the default SETTINGS_MAX_FRAME_SIZE (16384)
// minus the capsule type+length varint header.
const maxH2MTU = 16000

type Outbound struct {
	outbound.Adapter
	ctx         context.Context
	logger      logger.ContextLogger
	dialer      N.Dialer
	dnsRouter   adapter.DNSRouter
	profile     masque.Profile
	idleTimeout time.Duration

	server     M.Socksaddr
	uri        string
	network    string // "h3" | "h2"
	mtu        uint32
	prefixes   []netip.Prefix
	tlsConfig  *tls.Config
	quicConfig *quic.Config

	// Tunnel lifecycle. The current live tunnel is a *session; it is nil
	// whenever no tunnel is up (before the first dial, after an idle-suspend, or
	// after the tunnel died). A fresh session is built lazily on the next dial.
	// runMu guards sess and closed. (lx: SPEC 021 B1/C1/C2 — stateless idle,
	// self-healing reconnect, no leaked half-open tunnel.)
	runMu  sync.Mutex
	sess   *session
	closed bool
}

// session is one established MASQUE tunnel with its userspace stack and pumps.
type session struct {
	device wireguard.Device
	closer io.Closer
	ipConn masque.IpConn
	ctx    context.Context
	cancel context.CancelFunc
	// activity holds the UnixNano of the last packet in either direction, used
	// by the idle watcher. Atomic so pumps update it lock-free.
	activity atomic.Int64
	teardown sync.Once
}

func (s *session) markActivity(now int64) { s.activity.Store(now) }

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MASQUEOutboundOptions) (adapter.Outbound, error) {
	profile, err := masque.ParseProfile(options.Profile)
	if err != nil {
		return nil, err
	}

	network := options.Network
	if network == "" {
		network = "h3"
	}
	if network != "h3" && network != "h2" {
		return nil, E.New("masque: invalid network: ", network, " (expected h3 or h2)")
	}
	if network == "h2" && options.Profile == "standard" {
		return nil, E.New("masque: network h2 is not implemented for the standard profile")
	}

	prefixes, err := parsePrefixes(options.IP, options.IPv6)
	if err != nil {
		return nil, err
	}

	var privKey *ecdsa.PrivateKey
	var pubKey *ecdsa.PublicKey
	if profile.PinPublicKey {
		if options.PrivateKey == "" || options.PublicKey == "" {
			return nil, E.New("masque: private_key and public_key are required for the cloudflare profile")
		}
	}
	if options.PrivateKey != "" {
		privKey, err = parseECPrivateKey(options.PrivateKey)
		if err != nil {
			return nil, E.Cause(err, "parse private_key")
		}
	}
	if options.PublicKey != "" {
		pubKey, err = parseECPublicKey(options.PublicKey)
		if err != nil {
			return nil, E.Cause(err, "parse public_key")
		}
	}

	uri := options.URI
	if uri == "" {
		uri = profile.DefaultURI
	}
	if uri == "" {
		return nil, E.New("masque: uri is required for the standard profile")
	}

	sni := options.SNI
	if sni == "" {
		sni = profile.DefaultSNI
	}
	if sni == "" {
		sni = options.Server
	}

	tlsConfig, err := masque.PrepareTLSConfig(profile, privKey, pubKey, sni, options.SkipCertVerify)
	if err != nil {
		return nil, err
	}
	if network == "h2" {
		// PrepareTLSConfig defaults ALPN to h3; the h2 path needs h2.
		tlsConfig.NextProtos = []string{"h2"}
	}

	mtu := options.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}
	// On h2, each IP packet becomes one HTTP/2 DATA frame; a packet larger than
	// the default max frame size (16384) would be silently rejected (GOAWAY).
	// lx: SPEC 021 A5.
	if network == "h2" && mtu > maxH2MTU {
		return nil, E.New("masque: mtu ", mtu, " too large for h2 (max ", maxH2MTU, ")")
	}

	outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}

	// Idle-suspend window: after this long with no traffic the tunnel + stack +
	// pumps are torn down and rebuilt on the next dial. Default keeps a modest
	// window; 0 in config disables suspend (tunnel stays up until Close).
	idleTimeout := defaultIdleTimeout
	if options.IdleTimeout > 0 {
		idleTimeout = time.Duration(options.IdleTimeout)
	} else if options.IdleTimeout < 0 {
		idleTimeout = 0 // explicit disable
	}

	// QUIC keepalive: only needed to survive the server idle-timeout while the
	// tunnel is up. With idle-suspend on, a short window means we usually tear
	// down before keepalive matters; keep it configurable, default 30s.
	keepAlive := 30 * time.Second
	if options.KeepAlivePeriod > 0 {
		keepAlive = time.Duration(options.KeepAlivePeriod)
	} else if options.KeepAlivePeriod < 0 {
		keepAlive = 0
	}
	quicConfig := &quic.Config{
		EnableDatagrams:   true,
		InitialPacketSize: 1242,
		KeepAlivePeriod:   keepAlive,
	}

	networkList := options.NetworkList.Build()

	return &Outbound{
		Adapter:     outbound.NewAdapterWithDialerOptions(C.TypeMASQUE, tag, networkList, options.DialerOptions),
		ctx:         ctx,
		logger:      logger,
		dialer:      outboundDialer,
		dnsRouter:   service.FromContext[adapter.DNSRouter](ctx),
		profile:     profile,
		idleTimeout: idleTimeout,
		server:      options.ServerOptions.Build(),
		uri:         uri,
		network:     network,
		mtu:         mtu,
		prefixes:    prefixes,
		tlsConfig:   tlsConfig,
		quicConfig:  quicConfig,
	}, nil
}

// ensureSession returns a live tunnel session, building one lazily if none is
// up (first dial, or after an idle-suspend / tunnel death). Safe under
// concurrent dials — runMu serializes establishment, so only one tunnel is
// ever built per gap.
func (o *Outbound) ensureSession(ctx context.Context) (*session, error) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.closed {
		return nil, E.New("masque: outbound closed")
	}
	if o.sess != nil {
		return o.sess, nil
	}

	device, err := wireguard.NewDevice(wireguard.DeviceOptions{
		Context: o.ctx,
		Logger:  o.logger,
		MTU:     o.mtu,
		Address: o.prefixes,
	})
	if err != nil {
		return nil, E.Cause(err, "create userspace stack")
	}
	if err = device.Start(); err != nil {
		return nil, E.Cause(err, "start userspace stack")
	}

	var closer io.Closer
	var ipConn masque.IpConn
	switch o.network {
	case "h2":
		closer, ipConn, err = o.connectH2(ctx)
	default:
		closer, ipConn, err = o.connectH3(ctx)
	}
	if err != nil {
		_ = device.Close()
		return nil, err
	}

	sessCtx, cancel := context.WithCancel(o.ctx)
	s := &session{
		device: device,
		closer: closer,
		ipConn: ipConn,
		ctx:    sessCtx,
		cancel: cancel,
	}
	s.markActivity(time.Now().UnixNano())
	o.sess = s

	go o.pumpToTunnel(s)
	go o.pumpFromTunnel(s)
	if o.idleTimeout > 0 {
		go o.idleWatcher(s)
	}

	return s, nil
}

// teardownSession closes the session's tunnel + stack and clears it as current
// (if it still is). Idempotent per session (sync.Once) and generation-guarded:
// tearing down a stale session never disturbs a newer one. Closing ipConn also
// unblocks whichever pump is parked in a blocking read (context cancellation
// alone cannot interrupt ReceiveDatagram / recvCh). lx: SPEC 021 C1/C2.
func (o *Outbound) teardownSession(s *session) {
	s.teardown.Do(func() {
		s.cancel()
		if s.ipConn != nil {
			_ = s.ipConn.Close()
		}
		if s.closer != nil {
			_ = s.closer.Close()
		}
		if s.device != nil {
			_ = s.device.Close()
		}
		o.runMu.Lock()
		if o.sess == s {
			o.sess = nil // next dial rebuilds a fresh tunnel (self-healing)
		}
		o.runMu.Unlock()
	})
}

// idleWatcher suspends the tunnel after idleTimeout of no traffic, freeing the
// gVisor netstack, pumps and QUIC keepalive. The next dial rebuilds it. lx:
// SPEC 021 B1.
func (o *Outbound) idleWatcher(s *session) {
	// Poll at a fraction of the idle window (bounded) so suspend fires promptly
	// without a busy tick.
	interval := o.idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			idleFor := time.Duration(time.Now().UnixNano() - s.activity.Load())
			if idleFor >= o.idleTimeout {
				o.logger.DebugContext(o.ctx, "masque: idle-suspend tunnel after ", idleFor)
				o.teardownSession(s)
				return
			}
		}
	}
}

func (o *Outbound) connectH3(ctx context.Context) (io.Closer, masque.IpConn, error) {
	udpConn, err := o.dialer.DialContext(ctx, N.NetworkUDP, o.server)
	if err != nil {
		return nil, nil, E.Cause(err, "dial udp")
	}
	quicConn, err := quic.DialEarly(ctx, bufio.NewUnbindPacketConn(udpConn), udpConn.RemoteAddr(), o.tlsConfig, o.quicConfig)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, E.Cause(err, "dial quic")
	}
	tr, ipConn, err := masque.ConnectTunnelH3(ctx, o.profile, quicConn, o.uri)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, err
	}
	return &h3Closer{transport: tr, rawConn: udpConn}, ipConn, nil
}

func (o *Outbound) connectH2(ctx context.Context) (io.Closer, masque.IpConn, error) {
	c, err := o.dialer.DialContext(ctx, N.NetworkTCP, o.server)
	if err != nil {
		return nil, nil, E.Cause(err, "dial tcp")
	}
	tlsConn := tls.Client(c, o.tlsConfig)
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		_ = c.Close()
		return nil, nil, E.Cause(err, "tls handshake")
	}
	return masque.ConnectTunnelH2(ctx, o.profile, tlsConn, o.uri)
}

// pumpToTunnel reads outgoing IP packets from the userspace stack and writes
// them into the MASQUE tunnel. On any exit it tears the session down, which
// unblocks the paired pump and lets the next dial rebuild the tunnel.
func (o *Outbound) pumpToTunnel(s *session) {
	defer o.teardownSession(s)
	device := s.device
	batch := device.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, int(o.mtu)+80)
	}
	for s.ctx.Err() == nil {
		count, err := device.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		s.markActivity(time.Now().UnixNano())
		for i := 0; i < count; i++ {
			icmp, werr := s.ipConn.WritePacket(bufs[i][:sizes[i]])
			if werr != nil {
				return
			}
			if len(icmp) > 0 {
				_, _ = device.Write([][]byte{icmp}, 0)
			}
		}
	}
}

// pumpFromTunnel reads incoming IP packets from the MASQUE tunnel and injects
// them into the userspace stack.
func (o *Outbound) pumpFromTunnel(s *session) {
	defer o.teardownSession(s)
	for s.ctx.Err() == nil {
		packet, err := s.ipConn.ReadPacket()
		if err != nil {
			return
		}
		s.markActivity(time.Now().UnixNano())
		if _, err = s.device.Write([][]byte{packet}, 0); err != nil {
			return
		}
	}
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s, err := o.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	// The userspace stack operates at L3 and cannot resolve domains; resolve
	// here (as the WireGuard endpoint does) and dial the resulting IPs.
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, s.device, network, destination, addresses)
	}
	if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.device.DialContext(ctx, network, destination)
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s, err := o.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		packetConn, destinationAddress, err := N.ListenSerial(ctx, s.device, destination, addresses)
		if err != nil {
			return nil, err
		}
		if destinationAddress.IsValid() && destination != M.SocksaddrFrom(destinationAddress, destination.Port) {
			return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), M.SocksaddrFrom(destinationAddress, destination.Port), destination), nil
		}
		return packetConn, nil
	}
	if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return s.device.ListenPacket(ctx, destination)
}

func (o *Outbound) lookup(ctx context.Context, domain string) ([]netip.Addr, error) {
	if o.dnsRouter == nil {
		return nil, E.New("masque: no DNS router available to resolve ", domain)
	}
	return o.dnsRouter.Lookup(ctx, domain, adapter.DNSQueryOptions{})
}

func (o *Outbound) Close() error {
	o.runMu.Lock()
	if o.closed {
		o.runMu.Unlock()
		return nil
	}
	o.closed = true
	s := o.sess
	o.sess = nil
	o.runMu.Unlock()
	if s != nil {
		o.teardownSession(s)
	}
	return nil
}

type h3Closer struct {
	transport io.Closer
	rawConn   net.Conn
}

func (c *h3Closer) Close() error {
	if c.transport != nil {
		_ = c.transport.Close()
	}
	if c.rawConn != nil {
		_ = c.rawConn.Close()
	}
	return nil
}

func parsePrefixes(ip, ipv6 string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, 2)
	if ip != "" {
		if !strings.Contains(ip, "/") {
			ip += "/32"
		}
		prefix, err := netip.ParsePrefix(ip)
		if err != nil {
			return nil, E.Cause(err, "parse ip")
		}
		prefixes = append(prefixes, prefix)
	}
	if ipv6 != "" {
		if !strings.Contains(ipv6, "/") {
			ipv6 += "/128"
		}
		prefix, err := netip.ParsePrefix(ipv6)
		if err != nil {
			return nil, E.Cause(err, "parse ipv6")
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, E.New("masque: at least one of ip/ipv6 is required")
	}
	return prefixes, nil
}

func parseECPrivateKey(b64 string) (*ecdsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return x509.ParseECPrivateKey(der)
}

func parseECPublicKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, E.New("public_key is not an ECDSA key")
	}
	return ecPub, nil
}
