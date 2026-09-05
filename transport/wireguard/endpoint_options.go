package wireguard

import (
	"context"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/option" // lx: awg — option.AmneziaWGOptions
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type EndpointOptions struct {
	Context      context.Context
	Logger       logger.ContextLogger
	System       bool
	Handler      tun.Handler
	UDPTimeout   time.Duration
	ICMPTimeout  time.Duration
	UDPMapping   tun.NATMapping
	UDPFiltering tun.NATFiltering
	UDPNATMax    uint32

	InterfaceFinder   control.InterfaceFinder
	EgressPoolOptions tun.UDPEgressPoolOptions
	Dialer            N.Dialer
	CreateDialer      func(interfaceName string) N.Dialer
	Tag               string
	Name              string
	MTU               uint32
	Address           []netip.Prefix
	PrivateKey        string
	ListenPort        uint16
	ResolvePeer       func(domain string) ([]netip.Addr, error)
	Peers             []PeerOptions
	Workers           int
	// lx:begin awg
	// AmneziaWG 2.0 obfuscation parameters, carried from the endpoint options.
	// Consumed only under the `with_awg` build tag (see device_awg.go); with the
	// tag off, a non-empty value is rejected by device_stub_awg.go.
	AmneziaWG option.AmneziaWGOptions
	// lx:end awg
}

type PeerOptions struct {
	Endpoint                    M.Socksaddr
	PublicKey                   string
	PreSharedKey                string
	AllowedIPs                  []netip.Prefix
	PersistentKeepaliveInterval option.AWGRange // lx: awg — "N" or "min-max" (AWG 3.x), see option.WireGuardPeer
	Reserved                    []uint8
}
