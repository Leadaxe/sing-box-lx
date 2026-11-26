package group

import "github.com/sagernet/sing-box/adapter"

// lx: SPEC 139 — selectedOutboundTCP/UDP are written by performSelectionUpdate
// and by the dial path (moveSelection), and read by Select, Selected and
// References. Upstream made both fields atomic in 37fffa325; these helpers stay
// as the lx spelling of the same reads and writes.

func (g *URLTestGroup) selectedLx() (tcp, udp adapter.Outbound) {
	return g.selectedOutboundTCP.Load(), g.selectedOutboundUDP.Load()
}

func (g *URLTestGroup) setSelectedTCPLx(outbound adapter.Outbound) {
	g.selectedOutboundTCP.Store(outbound)
}

func (g *URLTestGroup) setSelectedUDPLx(outbound adapter.Outbound) {
	g.selectedOutboundUDP.Store(outbound)
}

// SelectsPerConnectionLx tells the router to hand a routed connection to this
// group instead of the node Selected reports (route/group_resolve_lx.go): the
// pick, penalties and the in-connection fallback run in DialContext.
func (s *URLTest) SelectsPerConnectionLx() {}
