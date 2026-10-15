package trusttunnel

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/trusttunnel"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// appName is the application string of the user agent and of every UDP frame.
const appName = "sing-box-lx"

// defaultIdleTimeout applies when neither the node nor lx.trusttunnel sets an
// idle window.
const defaultIdleTimeout = 5 * time.Minute

var _ adapter.InterfaceUpdateListener = (*Outbound)(nil)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.TrustTunnelOutboundOptions](registry, C.TypeTrustTunnel, NewOutbound)
}

type Outbound struct {
	outbound.Adapter
	logger       logger.ContextLogger
	client       *trusttunnel.Client
	dnsRouter    adapter.DNSRouter
	queryOptions adapter.DNSQueryOptions
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TrustTunnelOutboundOptions) (adapter.Outbound, error) {
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	if options.Username == "" {
		return nil, E.New("missing username")
	}
	if options.Password == "" {
		return nil, E.New("missing password")
	}
	networkList := options.Network.Build()
	for _, network := range networkList {
		switch network {
		case N.NetworkTCP, N.NetworkUDP:
		default:
			return nil, E.New("unsupported network: ", network, ", trusttunnel carries tcp and udp only")
		}
	}
	tlsOptions := *options.TLS
	if tlsOptions.ServerName == "" && M.ParseAddr(options.Server).IsValid() {
		return nil, E.New("tls.server_name is required when server is an IP address: the server routes by SNI and drops a ClientHello without it")
	}
	if options.QUIC && tlsOptions.UTLS != nil && tlsOptions.UTLS.Enabled {
		return nil, E.New("uTLS is not supported with quic")
	}
	switch options.QUICCongestionControl {
	case "", "bbr", "cubic", "reno":
	default:
		return nil, E.New("unknown quic_congestion_control: ", options.QUICCongestionControl)
	}
	protocol := trusttunnel.ProtocolHTTP2
	if options.QUIC {
		protocol = trusttunnel.ProtocolHTTP3
	}
	if len(tlsOptions.ALPN) == 0 {
		tlsOptions.ALPN = badoption.Listable[string]{protocol}
	} else if !common.Contains(tlsOptions.ALPN, protocol) {
		return nil, E.New("tls.alpn must contain ", protocol)
	}
	tlsConfig, err := tls.NewClientWithOptions(tls.ClientOptions{
		Context:             ctx,
		Logger:              logger,
		ServerAddress:       options.Server,
		Options:             tlsOptions,
		DialedThroughDetour: tls.DialedThroughDetour(options.DialerOptions),
	})
	if err != nil {
		return nil, err
	}
	outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}
	queryOptions, err := udpQueryOptions(ctx, options.DialerOptions)
	if err != nil {
		return nil, err
	}
	client, err := trusttunnel.NewClient(trusttunnel.ClientOptions{
		Context:   ctx,
		Logger:    logger,
		Dialer:    outboundDialer,
		Server:    options.ServerOptions.Build(),
		TLSConfig: tlsConfig,
		Protocol:  protocol,
		QUIC: &trusttunnel.QUICOptions{
			CongestionControl: options.QUICCongestionControl,
		},
		Username:    options.Username,
		Password:    options.Password,
		AppName:     appName,
		HealthCheck: options.HealthCheck,
		IdleTimeout: idleWindow(options.IdleTimeout, service.PtrFromContext[option.LXResolved](ctx).TrustTunnelOrZero().IdleTimeout),
	})
	if err != nil {
		return nil, err
	}
	return &Outbound{
		Adapter:      outbound.NewAdapterWithDialerOptions(C.TypeTrustTunnel, tag, networkList, options.DialerOptions),
		logger:       logger,
		client:       client,
		dnsRouter:    service.FromContext[adapter.DNSRouter](ctx),
		queryOptions: queryOptions,
	}, nil
}

// idleWindow picks the session's idle teardown: the node's own idle_timeout
// when set (an explicit "0" disables teardown), otherwise the global
// lx.trusttunnel.idle_timeout, otherwise five minutes.
func idleWindow(node *badoption.Duration, global time.Duration) time.Duration {
	if node != nil {
		return max(time.Duration(*node), 0)
	}
	if global > 0 {
		return global
	}
	return defaultIdleTimeout
}

// udpQueryOptions resolves the DNS options for UDP destination domains the
// way the dial fields resolve the server address: the node's domain_resolver,
// else route.default_domain_resolver, else the DNS router's own rules.
func udpQueryOptions(ctx context.Context, options option.DialerOptions) (adapter.DNSQueryOptions, error) {
	hasDomainResolver := options.DomainResolver != nil && options.DomainResolver.Server != ""
	if !hasDomainResolver {
		networkManager := service.FromContext[adapter.NetworkManager](ctx)
		if networkManager == nil || networkManager.DefaultOptions().DomainResolver == "" {
			return adapter.DNSQueryOptions{}, nil
		}
	}
	return dialer.NewDNSQueryOptions(ctx, options.DomainResolver, false)
}

func (h *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		h.logger.InfoContext(ctx, "outbound connection to ", destination)
		return h.client.DialContext(ctx, destination)
	case N.NetworkUDP:
		conn, err := h.ListenPacket(ctx, destination)
		if err != nil {
			return nil, err
		}
		return bufio.NewBindPacketConn(conn, destination), nil
	default:
		return nil, E.New("unsupported network: ", network)
	}
}

// ListenPacket opens a _udp2 stream. Frames carry IP addresses only, so a
// domain destination is resolved here and mapped back on the way in.
func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	h.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	var resolved netip.Addr
	if destination.IsDomain() {
		address, err := h.lookup(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		resolved = address
	}
	packetConn, err := h.client.ListenPacket(ctx)
	if err != nil {
		return nil, err
	}
	if resolved.IsValid() {
		return bufio.NewNATPacketConn(packetConn, M.SocksaddrFrom(resolved, destination.Port), destination), nil
	}
	return packetConn, nil
}

func (h *Outbound) lookup(ctx context.Context, domain string) (netip.Addr, error) {
	if h.dnsRouter == nil {
		return netip.Addr{}, E.New("missing DNS router to resolve ", domain)
	}
	addresses, err := h.dnsRouter.Lookup(log.ContextWithOverrideLevel(ctx, log.LevelDebug), domain, h.queryOptions)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addresses) == 0 {
		return netip.Addr{}, E.New("resolve ", domain, ": empty result")
	}
	return addresses[0], nil
}

// InterfaceUpdated drops the session: QUIC migration is off on the official
// endpoint, and an HTTP/2 session on the old network is dead anyway.
func (h *Outbound) InterfaceUpdated(ctx context.Context) {
	h.client.Reset()
}

func (h *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateInitialize {
		scope.Add(h.Close)
	}
	return nil
}

func (h *Outbound) Close() error {
	return h.client.Close()
}
