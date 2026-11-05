package masque

// lx: SPEC 110 — the Cloudflare WARP dialect of CONNECT-IP on the client side.

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/sagernet/sing/common"
)

// ClientLXOptions are set by the masque-client endpoint from its `profile` and
// `address`.
type ClientLXOptions struct {
	// Protocol is the :protocol token of the tunnel request (and the Upgrade
	// token on HTTP/1.1). Empty means RFC 9484's connect-ip; WARP wants
	// cf-connect-ip.
	Protocol string
	// Address are the tunnel's own addresses when they are not assigned
	// in-band. With it set the client sends no ADDRESS_REQUEST, becomes ready
	// as soon as the tunnel is open, and ignores ADDRESS_ASSIGN.
	Address []netip.Prefix
}

func (c *Client) tunnelProtocol() string {
	if c.lx.Protocol != "" {
		return c.lx.Protocol
	}
	return upgradeToken
}

// requestOrApplyAddress is the first capsule exchange of a session: ask the
// server for addresses, or, with static ones configured, apply them now.
func (s *clientSession) requestOrApplyAddress() error {
	if len(s.client.lx.Address) == 0 {
		return s.writeCapsule(newAddressCapsule(capsuleTypeAddressRequest, []AssignedAddress{
			{RequestID: 1, Prefix: netip.PrefixFrom(netip.IPv4Unspecified(), 32)},
			{RequestID: 2, Prefix: netip.PrefixFrom(netip.IPv6Unspecified(), 128)},
		}))
	}
	s.access.Lock()
	s.configuration.Address = slices.Clone(s.client.lx.Address)
	configuration := s.configuration
	s.access.Unlock()
	return s.updateConfiguration(configuration)
}

// ignoreAddressAssign reports whether an ADDRESS_ASSIGN is to be dropped
// because the addresses are configured statically. A server that assigns
// something else is logged once per session so a mismatch is visible.
func (c *Client) ignoreAddressAssign(addresses []AssignedAddress) bool {
	if len(c.lx.Address) == 0 {
		return false
	}
	assigned := common.Map(addresses, func(it AssignedAddress) string { return it.Prefix.String() })
	configured := common.Map(c.lx.Address, func(it netip.Prefix) string { return it.String() })
	if !slices.Equal(assigned, configured) {
		c.logger.Debug("ignoring ADDRESS_ASSIGN ", strings.Join(assigned, " "), ": addresses are configured as ", strings.Join(configured, " "))
	}
	return true
}
