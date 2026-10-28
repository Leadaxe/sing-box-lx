package adapter

import N "github.com/sagernet/sing/common/network"

// GroupNow is the tag an outbound group points at right now: the TCP pick,
// else the UDP one, empty when the group has none. lx: what Now() returned
// before upstream 03335338a replaced it with Selected(network); the lx
// readers (detour chain, reachability, chain unfolding, status RPCs) want one
// network-agnostic answer.
func GroupNow(group OutboundGroup) string {
	selected := group.Selected(N.NetworkTCP)
	if selected == nil {
		selected = group.Selected(N.NetworkUDP)
	}
	if selected == nil {
		return ""
	}
	return selected.Tag()
}
