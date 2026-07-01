package masque

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
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

type Outbound struct {
	outbound.Adapter
	ctx       context.Context
	logger    logger.ContextLogger
	dialer    N.Dialer
	dnsRouter adapter.DNSRouter
	profile   masque.Profile

	server     M.Socksaddr
	uri        string
	network    string // "h3" | "h2"
	mtu        uint32
	prefixes   []netip.Prefix
	tlsConfig  *tls.Config
	quicConfig *quic.Config

	// lazily-started tunnel state
	runMu   sync.Mutex
	running bool
	device  wireguard.Device
	closer  io.Closer
	ipConn  masque.IpConn
	runCtx  context.Context
	cancel  context.CancelFunc
}

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

	mtu := options.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}

	outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}

	quicConfig := &quic.Config{
		EnableDatagrams:   true,
		InitialPacketSize: 1242,
		KeepAlivePeriod:   30 * time.Second,
	}

	networkList := options.NetworkList.Build()

	return &Outbound{
		Adapter:    outbound.NewAdapterWithDialerOptions(C.TypeMASQUE, tag, networkList, options.DialerOptions),
		ctx:        ctx,
		logger:     logger,
		dialer:     outboundDialer,
		dnsRouter:  service.FromContext[adapter.DNSRouter](ctx),
		profile:    profile,
		server:     options.ServerOptions.Build(),
		uri:        uri,
		network:    network,
		mtu:        mtu,
		prefixes:   prefixes,
		tlsConfig:  tlsConfig,
		quicConfig: quicConfig,
	}, nil
}

// run lazily establishes the tunnel and userspace stack on first dial.
func (o *Outbound) run(ctx context.Context) error {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.running {
		return nil
	}

	device, err := wireguard.NewDevice(wireguard.DeviceOptions{
		Context: o.ctx,
		Logger:  o.logger,
		MTU:     o.mtu,
		Address: o.prefixes,
	})
	if err != nil {
		return E.Cause(err, "create userspace stack")
	}
	if err = device.Start(); err != nil {
		return E.Cause(err, "start userspace stack")
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
		return err
	}

	runCtx, cancel := context.WithCancel(o.ctx)
	o.device = device
	o.closer = closer
	o.ipConn = ipConn
	o.runCtx = runCtx
	o.cancel = cancel
	o.running = true

	go o.pumpToTunnel(runCtx, device, ipConn)
	go o.pumpFromTunnel(runCtx, device, ipConn)

	return nil
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
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	transport := &http.Transport{
		Protocols: protocols,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := o.dialer.DialContext(ctx, N.NetworkTCP, o.server)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(c, o.tlsConfig)
			if err = tlsConn.HandshakeContext(ctx); err != nil {
				_ = c.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
	return masque.ConnectTunnelH2(ctx, o.profile, transport, o.uri)
}

// pumpToTunnel reads outgoing IP packets from the userspace stack and writes
// them into the MASQUE tunnel.
func (o *Outbound) pumpToTunnel(ctx context.Context, device wireguard.Device, ipConn masque.IpConn) {
	defer o.cancel()
	batch := device.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, int(o.mtu)+80)
	}
	for ctx.Err() == nil {
		count, err := device.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		for i := 0; i < count; i++ {
			icmp, werr := ipConn.WritePacket(bufs[i][:sizes[i]])
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
func (o *Outbound) pumpFromTunnel(ctx context.Context, device wireguard.Device, ipConn masque.IpConn) {
	defer o.cancel()
	for ctx.Err() == nil {
		packet, err := ipConn.ReadPacket()
		if err != nil {
			return
		}
		if _, err = device.Write([][]byte{packet}, 0); err != nil {
			return
		}
	}
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := o.run(ctx); err != nil {
		return nil, err
	}
	// The userspace stack operates at L3 and cannot resolve domains; resolve
	// here (as the WireGuard endpoint does) and dial the resulting IPs.
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, o.device, network, destination, addresses)
	}
	if !destination.Addr.IsValid() {
		return nil, E.New("invalid destination: ", destination)
	}
	return o.device.DialContext(ctx, network, destination)
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if err := o.run(ctx); err != nil {
		return nil, err
	}
	if destination.IsDomain() {
		addresses, err := o.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		packetConn, destinationAddress, err := N.ListenSerial(ctx, o.device, destination, addresses)
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
	return o.device.ListenPacket(ctx, destination)
}

func (o *Outbound) lookup(ctx context.Context, domain string) ([]netip.Addr, error) {
	if o.dnsRouter == nil {
		return nil, E.New("masque: no DNS router available to resolve ", domain)
	}
	return o.dnsRouter.Lookup(ctx, domain, adapter.DNSQueryOptions{})
}

func (o *Outbound) Close() error {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if !o.running {
		return nil
	}
	o.running = false
	if o.cancel != nil {
		o.cancel()
	}
	if o.ipConn != nil {
		_ = o.ipConn.Close()
	}
	if o.closer != nil {
		_ = o.closer.Close()
	}
	if o.device != nil {
		_ = o.device.Close()
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
