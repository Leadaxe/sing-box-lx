package interrupt

import (
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

// lx: SPEC 064 — N.PacketConn variant, needed to register the inbound packet
// conn in Selector.NewPacketConnection (ported from upstream PR #4285).
func (g *Group) NewSingPacketConn(conn N.PacketConn, isExternal bool) N.PacketConn {
	g.access.Lock()
	defer g.access.Unlock()
	item := g.connections.PushBack(&groupConnItem{conn, isExternal})
	return &SingPacketConn{PacketConn: conn, group: g, element: item}
}

// lx: SPEC 064 — wrapper over sing's N.PacketConn (ported from upstream PR #4285).
type SingPacketConn struct {
	N.PacketConn
	group   *Group
	element *list.Element[*groupConnItem]
}

// Detach under the lock, close after it, as Conn.Close does: a registered conn
// may itself be another group's wrapper, and closing under the mutex takes two
// group locks in opposite orders. lx: SPEC 084.
func (c *SingPacketConn) Close() error {
	c.group.access.Lock()
	c.group.connections.Remove(c.element)
	c.group.access.Unlock()
	return c.PacketConn.Close()
}

func (c *SingPacketConn) ReaderReplaceable() bool {
	return true
}

func (c *SingPacketConn) WriterReplaceable() bool {
	return true
}

func (c *SingPacketConn) Upstream() any {
	return c.PacketConn
}
