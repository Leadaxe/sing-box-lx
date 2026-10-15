package trusttunnel

import (
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ N.NetPacketConn = (*PacketConn)(nil)
	_ N.FrontHeadroom = (*PacketConn)(nil)
)

// PacketConn is the _udp2 stream of a client: every datagram is one frame.
// Destinations must be IP addresses. Frames are sent with a zero source, so all
// datagrams of the conn to one destination form one flow on the server.
type PacketConn struct {
	conn        *streamConn
	reader      frameReader
	appName     string
	headroom    int
	server      M.Socksaddr
	readAccess  sync.Mutex
	writeAccess sync.Mutex
}

func newPacketConn(conn *streamConn, appName string, server M.Socksaddr) *PacketConn {
	packetConn := &PacketConn{
		conn:     conn,
		appName:  appName,
		headroom: ClientFrameHeadroom(appName),
		server:   server,
	}
	packetConn.reader.reader = conn
	return packetConn
}

func (c *PacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	source, _, err := c.reader.ReadServerFrame(buffer)
	if err != nil {
		return M.Socksaddr{}, c.readError(err)
	}
	return source, nil
}

func (c *PacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if !destination.IsIP() {
		buffer.Release()
		return E.New("trusttunnel: UDP destination is not an IP address: ", destination)
	}
	if buffer.Start() < c.headroom {
		newBuffer := buf.NewSize(c.headroom + buffer.Len())
		newBuffer.Advance(c.headroom)
		_, err := newBuffer.Write(buffer.Bytes())
		buffer.Release()
		if err != nil {
			newBuffer.Release()
			return err
		}
		buffer = newBuffer
	}
	defer buffer.Release()
	err := EncodeClientFrame(buffer, M.Socksaddr{}, destination, c.appName)
	if err != nil {
		return err
	}
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	_, err = c.conn.Write(buffer.Bytes())
	return err
}

func (c *PacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	n, source, err := c.reader.ReadServerFrameTo(p)
	if err != nil {
		return 0, nil, c.readError(err)
	}
	return n, source.UDPAddr(), nil
}

// readError closes the stream on a malformed frame: it cannot be resynced.
func (c *PacketConn) readError(err error) error {
	if isFrameError(err) {
		c.conn.Close()
	}
	return err
}

func (c *PacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if len(p) > MaxPayloadSize {
		return 0, errFrameTooLarge
	}
	buffer := buf.NewSize(c.headroom + len(p))
	buffer.Advance(c.headroom)
	common.Must1(buffer.Write(p))
	err = c.WritePacket(buffer, M.SocksaddrFromNet(addr).Unwrap())
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *PacketConn) FrontHeadroom() int {
	return c.headroom
}

func (c *PacketConn) Close() error {
	return c.conn.Close()
}

func (c *PacketConn) LocalAddr() net.Addr {
	return c.server
}

func (c *PacketConn) RemoteAddr() net.Addr {
	return c.server
}

func (c *PacketConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *PacketConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *PacketConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

func (c *PacketConn) NeedAdditionalReadDeadline() bool {
	return c.conn.NeedAdditionalReadDeadline()
}
