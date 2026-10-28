package group

import "github.com/sagernet/sing-box/adapter"

// lx: SPEC 139 — selectedOutboundTCP/UDP are written by performSelectionUpdate
// (under updateAccess, which may be held across a failover probe) and read by
// Select, Now and References without any lock upstream. SPEC 136 moved the
// reference manager's first update() onto a goroutine, so References now races
// the first selection in practice (-race on the SPEC 139 stand). A dedicated
// RWMutex keeps the readers off updateAccess so they never wait for a probe.

func (g *URLTestGroup) selectedLx() (tcp, udp adapter.Outbound) {
	g.selectedAccess.RLock()
	defer g.selectedAccess.RUnlock()
	return g.selectedOutboundTCP, g.selectedOutboundUDP
}

func (g *URLTestGroup) setSelectedTCPLx(outbound adapter.Outbound) {
	g.selectedAccess.Lock()
	g.selectedOutboundTCP = outbound
	g.selectedAccess.Unlock()
}

func (g *URLTestGroup) setSelectedUDPLx(outbound adapter.Outbound) {
	g.selectedAccess.Lock()
	g.selectedOutboundUDP = outbound
	g.selectedAccess.Unlock()
}

// SelectsPerConnectionLx tells the router to hand a routed connection to this
// group instead of the node Selected reports (route/group_resolve_lx.go): the
// pick, penalties and the in-connection fallback run in DialContext.
func (s *URLTest) SelectsPerConnectionLx() {}
