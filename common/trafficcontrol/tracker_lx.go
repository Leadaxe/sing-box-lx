package trafficcontrol

import (
	"slices"
	"sync/atomic"

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
// continued through any group the router stopped at (route/group_resolve_lx.go:
// a urltest group picks its node per connection in DialContext) by the member
// the group picked for this connection (SPEC 138, metadata.ActualOutboundLx),
// else by the node the group points at right now — what the chain showed while
// it was walked with Now(). An empty chain (a caller that did not route)
// starts from the matched outbound, else the default one.
func (m *Manager) displayChainLx(metadata adapter.InboundContext, matchOutbound adapter.Outbound) []adapter.Outbound {
	return m.displayChainPicksLx(metadata, matchOutbound, metadata.ActualOutboundLx.Load())
}

func (m *Manager) displayChainPicksLx(metadata adapter.InboundContext, matchOutbound adapter.Outbound, picks *adapter.ActualPicksLx) []adapter.Outbound {
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
		next := picks.Picked(group.Tag())
		if next == nil {
			next = group.Selected(metadata.Network)
		}
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
// group is descended through the member that group picked for this connection
// when the dial recorded it (SPEC 138), else through the node it points at
// right now. The routing chain's tags are never revisited, which also guards
// against detour cycles. Order: final outbound → outward.
func (m *Manager) detourChainLx(finalOutbound adapter.Outbound, routing []adapter.Outbound) []string {
	return m.detourChainPicksLx(finalOutbound, routing, nil)
}

func (m *Manager) detourChainPicksLx(finalOutbound adapter.Outbound, routing []adapter.Outbound, picks *adapter.ActualPicksLx) []string {
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
			if picked := picks.Picked(dependencies[0]); picked != nil {
				now = picked.Tag()
			}
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

// actualViewLx ties a tracker to the SPEC 138 cell of its connection and
// caches the metadata view derived from the cell's latest state. The base
// TrackerMetadata stays as newTrackerMetadata built it (the node the group
// pointed at when the entry was created); once a group records its pick, the
// readers get an immutable copy with Chain, Outbound, OutboundType and Detour
// recomputed from the picks. A new pick (a retry) yields a new copy, so no
// field a reader may be reading is ever written.
type actualViewLx struct {
	cell  *adapter.ActualOutboundLx
	cache atomic.Pointer[actualViewCacheLx]
}

type actualViewCacheLx struct {
	picks *adapter.ActualPicksLx
	view  *TrackerMetadata
}

func newActualViewLx(metadata adapter.InboundContext) *actualViewLx {
	if metadata.ActualOutboundLx == nil || len(metadata.OutboundChain) == 0 {
		return nil
	}
	return &actualViewLx{cell: metadata.ActualOutboundLx}
}

// actualLx is what Metadata() hands out: the base metadata while no group has
// recorded a pick, the cached view of the latest picks after that.
func (m *Manager) actualLx(base *TrackerMetadata) *TrackerMetadata {
	holder := base.actualLx
	if holder == nil {
		return base
	}
	picks := holder.cell.Load()
	if picks == nil {
		return base
	}
	cached := holder.cache.Load()
	if cached != nil && cached.picks == picks {
		return cached.view
	}
	view := *base
	outboundChain := m.displayChainPicksLx(base.Metadata, nil, picks)
	chain := common.Map(outboundChain, adapter.Outbound.Tag)
	slices.Reverse(chain)
	outbound := outboundChain[len(outboundChain)-1]
	view.Chain = chain
	view.Outbound = outbound.Tag()
	view.OutboundType = outbound.Type()
	view.Detour = m.detourChainPicksLx(outbound, outboundChain, picks)
	entry := &actualViewCacheLx{picks: picks, view: &view}
	if holder.cache.CompareAndSwap(cached, entry) {
		return &view
	}
	// another reader built the view first: hand out that one, so a reader that
	// compares views by identity (the daemon) sees one view per state
	if winner := holder.cache.Load(); winner != nil && winner.picks == picks {
		return winner.view
	}
	return &view
}

// IsActualViewLx reports whether t is the current SPEC 138 view of its
// connection: a copy built from the picks of a per-connection group, distinct
// from the metadata the connection was announced with.
func (t *TrackerMetadata) IsActualViewLx() bool {
	if t.actualLx == nil {
		return false
	}
	cached := t.actualLx.cache.Load()
	return cached != nil && cached.view == t
}
