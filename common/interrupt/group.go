package interrupt

import (
	"io"
	"net"
	"sync"

	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

type Group struct {
	access      sync.Mutex
	connections list.List[*groupConnItem]
}

type groupConnItem struct {
	conn       io.Closer
	isExternal bool
}

func NewGroup() *Group {
	return &Group{}
}

func (g *Group) NewConn(conn net.Conn, isExternal bool) net.Conn {
	g.access.Lock()
	defer g.access.Unlock()
	item := g.connections.PushBack(&groupConnItem{conn, isExternal})
	return &Conn{Conn: conn, group: g, element: item}
}

func (g *Group) NewPacketConn(conn net.PacketConn, isExternal bool) net.PacketConn {
	g.access.Lock()
	defer g.access.Unlock()
	item := g.connections.PushBack(&groupConnItem{conn, isExternal})
	return newPacketConn(g, conn, item)
}

// lx: SPEC 064 — N.PacketConn variant, needed to register the inbound packet
// conn in Selector.NewPacketConnection (ported from upstream PR #4285).
func (g *Group) NewSingPacketConn(conn N.PacketConn, isExternal bool) N.PacketConn {
	g.access.Lock()
	defer g.access.Unlock()
	item := g.connections.PushBack(&groupConnItem{conn, isExternal})
	return &SingPacketConn{PacketConn: conn, group: g, element: item}
}

// lx: SPEC 084 — the underlying Close must run OUTSIDE g.access. A registered
// conn may itself be another group's wrapper (nested selectors wrap the inbound
// side outer→inner and the dial side inner→outer), so closing under the mutex
// takes two group locks in opposite orders → ABBA deadlock (issue #20). The
// sweep therefore detaches the entries under the lock and closes them after
// releasing it; a concurrent wrapper Close on the same entry is harmless (list
// Remove is a no-op for a detached element, double Close is tolerated by conns).
func (g *Group) Interrupt(interruptExternalConnections bool) {
	g.access.Lock()
	var toClose []io.Closer
	for element := g.connections.Front(); element != nil; {
		next := element.Next()
		if !element.Value.isExternal || interruptExternalConnections {
			toClose = append(toClose, element.Value.conn)
			g.connections.Remove(element)
		}
		element = next
	}
	g.access.Unlock()
	for _, conn := range toClose {
		conn.Close()
	}
}
