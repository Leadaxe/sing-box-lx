package option

import (
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
)

type TrustTunnelInboundOptions struct {
	ListenOptions
	Users                 []auth.User `json:"users,omitempty"`
	Network               NetworkList `json:"network,omitempty"`
	QUICCongestionControl string      `json:"quic_congestion_control,omitempty" enum:"bbr,cubic,reno"`
	InboundTLSOptionsContainer
}

type TrustTunnelOutboundOptions struct {
	DialerOptions
	ServerOptions
	Username              string              `json:"username,omitempty"`
	Password              string              `json:"password,omitempty"`
	Network               NetworkList         `json:"network,omitempty"`
	QUIC                  bool                `json:"quic,omitempty"`
	QUICCongestionControl string              `json:"quic_congestion_control,omitempty" enum:"bbr,cubic,reno"`
	HealthCheck           bool                `json:"health_check,omitempty"`
	IdleTimeout           *badoption.Duration `json:"idle_timeout,omitempty"`
	OutboundTLSOptionsContainer
}
