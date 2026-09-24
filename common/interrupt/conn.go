package interrupt

import (
	"net"

	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

type Conn struct {
	net.Conn
	group   *Group
	element *list.Element[*groupConnItem]
}

// lx: SPEC 084 — detach under the lock, close after it (see Group.Interrupt).
func (c *Conn) Close() error {
	c.group.access.Lock()
	c.group.connections.Remove(c.element)
	c.group.access.Unlock()
	return c.Conn.Close()
}

func (c *Conn) ReaderReplaceable() bool {
	return true
}

func (c *Conn) WriterReplaceable() bool {
	return true
}

func (c *Conn) Upstream() any {
	return c.Conn
}

type PacketConn struct {
	N.NetPacketConn
	group   *Group
	element *list.Element[*groupConnItem]
}

func newPacketConn(group *Group, conn net.PacketConn, element *list.Element[*groupConnItem]) *PacketConn {
	return &PacketConn{NetPacketConn: bufio.NewPacketConn(conn), group: group, element: element}
}

// lx: SPEC 084 — detach under the lock, close after it (see Group.Interrupt).
func (c *PacketConn) Close() error {
	c.group.access.Lock()
	c.group.connections.Remove(c.element)
	c.group.access.Unlock()
	return c.NetPacketConn.Close()
}

func (c *PacketConn) ReaderReplaceable() bool {
	return true
}

func (c *PacketConn) WriterReplaceable() bool {
	return true
}

func (c *PacketConn) Upstream() any {
	return c.NetPacketConn
}

// lx: SPEC 064 — wrapper over sing's N.PacketConn (ported from upstream PR #4285).
type SingPacketConn struct {
	N.PacketConn
	group   *Group
	element *list.Element[*groupConnItem]
}

// lx: SPEC 084 — detach under the lock, close after it (see Group.Interrupt).
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
