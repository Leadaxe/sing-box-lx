package route

import (
	"github.com/sagernet/sing-box/adapter"
)

// perConnectionGroupLx marks a group that picks its node for every connection
// in its own DialContext / ListenPacket: the urltest group of this fork, whose
// penalties, in-connection fallback on a failed dial (SPEC 054), round_robin
// slots (SPEC 019) and failover mode (SPEC 116) all run there.
type perConnectionGroupLx interface {
	SelectsPerConnectionLx()
}

// resolveOutboundLx is resolveOutbound for a routed connection, cut after the
// first group that picks per connection: the router then hands the connection
// to that group, as it did before upstream 03335338a resolved groups itself.
// The groups before the cut still get the connection attached for interrupts.
// The network check of resolveOutbound stays on the node the group points at
// right now. Pre-match flows keep resolveOutbound: they bypassed the group's
// dial before the change as well.
func resolveOutboundLx(outbound adapter.Outbound, network string) ([]adapter.Outbound, error) {
	chain, err := resolveOutbound(outbound, network)
	if err != nil {
		return nil, err
	}
	for index, element := range chain[:len(chain)-1] {
		if _, isPerConnection := element.(perConnectionGroupLx); isPerConnection {
			return chain[:index+1], nil
		}
	}
	return chain, nil
}
