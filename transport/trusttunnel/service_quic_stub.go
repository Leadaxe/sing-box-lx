//go:build !with_quic

package trusttunnel

import (
	"net"

	aTLS "github.com/sagernet/sing/common/tls"
)

// ServeQUIC needs the with_quic build tag.
func (s *Service) ServeQUIC(net.PacketConn, aTLS.ServerConfig) error {
	return ErrQUICNotIncluded
}
