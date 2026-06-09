package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

type WireGuardEndpointOptions struct {
	System     bool                             `json:"system,omitempty"`
	Name       string                           `json:"name,omitempty"`
	MTU        uint32                           `json:"mtu,omitempty"`
	Address    badoption.Listable[netip.Prefix] `json:"address"`
	PrivateKey string                           `json:"private_key"`
	ListenPort uint16                           `json:"listen_port,omitempty"`
	Peers      []WireGuardPeer                  `json:"peers,omitempty"`
	UDPTimeout badoption.Duration               `json:"udp_timeout,omitempty"`
	Workers    int                              `json:"workers,omitempty"`
	// lx:begin awg
	// AmneziaWG 2.0 obfuscation parameters (jc/jmin/jmax/s1/s2/h1..h4/i1..i5).
	// Embedded (no wrapper key) so AWG fields are promoted to the endpoint root,
	// matching awg-quick/.conf layout — same pattern as DialerOptions below.
	// Effective only with the `with_awg` build tag; otherwise setting any field
	// is an explicit error (see device gating in transport/wireguard).
	AmneziaWGOptions
	// lx:end awg
	DialerOptions
}

type WireGuardPeer struct {
	Address                     string                           `json:"address,omitempty"`
	Port                        uint16                           `json:"port,omitempty"`
	PublicKey                   string                           `json:"public_key,omitempty"`
	PreSharedKey                string                           `json:"pre_shared_key,omitempty"`
	AllowedIPs                  badoption.Listable[netip.Prefix] `json:"allowed_ips,omitempty"`
	PersistentKeepaliveInterval uint16                           `json:"persistent_keepalive_interval,omitempty"`
	Reserved                    []uint8                          `json:"reserved,omitempty"`
}
