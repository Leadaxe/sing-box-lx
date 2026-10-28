package trafficcontrol

import (
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
)

// SetOutboundManagerLx hands the manager the outbound lookup the detour tail
// walk needs: upstream 03335338a dropped the manager's own reference when the
// routing chain moved into metadata.OutboundChain.
func (m *Manager) SetOutboundManagerLx(outbound adapter.OutboundManager) {
	m.outboundLx = outbound
}

// displayChainLx is the routing chain a tracker reports: metadata.OutboundChain,
// continued through any group the router stopped at (route/conn_lx.go: a
// urltest group picks its node per connection in DialContext) by the node the
// group points at right now — what the chain showed while it was walked with
// Now(). An empty chain (a caller that did not route) starts from the matched
// outbound, else the default one.
func (m *Manager) displayChainLx(metadata adapter.InboundContext, matchOutbound adapter.Outbound) []adapter.Outbound {
	chain := metadata.OutboundChain
	if len(chain) == 0 {
		if matchOutbound == nil && m.outboundLx != nil {
			matchOutbound = m.outboundLx.Default()
		}
		if matchOutbound == nil {
			return nil
		}
		chain = []adapter.Outbound{matchOutbound}
	}
	seen := make(map[string]bool, len(chain))
	for _, outbound := range chain {
		seen[outbound.Tag()] = true
	}
	for {
		group, isGroup := chain[len(chain)-1].(adapter.OutboundGroup)
		if !isGroup {
			return chain
		}
		next := group.Selected(metadata.Network)
		if next == nil || seen[next.Tag()] {
			return chain
		}
		seen[next.Tag()] = true
		chain = append(chain[:len(chain):len(chain)], next)
	}
}

// detourChainLx walks the detour tail of the final outbound (SPEC 017): the
// part Chain omits by design. Dependencies()[0] of a non-group outbound is
// exactly its detour (adapter/outbound/adapter.go); a detour that points at a
// group is descended through the node it points at right now. The routing
// chain's tags are never revisited, which also guards against detour cycles.
// Order: final outbound → outward.
func (m *Manager) detourChainLx(finalOutbound adapter.Outbound, routing []adapter.Outbound) []string {
	if m.outboundLx == nil {
		return nil
	}
	if _, isGroup := finalOutbound.(adapter.OutboundGroup); isGroup {
		return nil
	}
	// SPEC 073: a chain outbound reports its resolved path itself (by position,
	// entry first); its Dependencies()[0] is the entry, not a detour.
	if pathProvider, isChain := finalOutbound.(adapter.ChainPathProvider); isChain {
		return common.Reverse(pathProvider.ChainPath())
	}
	seen := make(map[string]bool, len(routing))
	for _, outbound := range routing {
		seen[outbound.Tag()] = true
	}
	var detourChain []string
	for current := finalOutbound; current != nil; {
		dependencies := current.Dependencies()
		if len(dependencies) == 0 || seen[dependencies[0]] {
			break
		}
		step, loaded := m.outboundLx.Outbound(dependencies[0])
		if !loaded {
			break
		}
		detourChain = append(detourChain, dependencies[0])
		seen[dependencies[0]] = true
		if group, isGroup := step.(adapter.OutboundGroup); isGroup {
			now := adapter.GroupNow(group)
			if now == "" || seen[now] {
				break
			}
			nowOutbound, loaded := m.outboundLx.Outbound(now)
			if !loaded {
				break
			}
			detourChain = append(detourChain, now)
			seen[now] = true
			current = nowOutbound
			continue
		}
		current = step
	}
	return detourChain
}
