package wireguard

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
	"github.com/sagernet/wireguard-go/conn"
)

var _ conn.Bind = (*ClientBind)(nil)

type ClientBind struct {
	ctx                 context.Context
	logger              logger.Logger
	pauseManager        pause.Manager
	bindCtx             context.Context
	bindDone            context.CancelFunc
	dialer              N.Dialer
	reservedForEndpoint map[netip.AddrPort][3]uint8
	connAccess          sync.Mutex
	conn                atomic.Pointer[wireConn] // lx: atomic — read on the connect() fast-path is lock-free (was a data race, upstream)
	done                chan struct{}
	isConnect           bool
	connectAddr         netip.AddrPort
	reserved            [3]uint8
}

func NewClientBind(ctx context.Context, logger logger.Logger, dialer N.Dialer, isConnect bool, connectAddr netip.AddrPort, reserved [3]uint8) *ClientBind {
	return &ClientBind{
		ctx:                 ctx,
		logger:              logger,
		pauseManager:        service.FromContext[pause.Manager](ctx),
		dialer:              dialer,
		reservedForEndpoint: make(map[netip.AddrPort][3]uint8),
		done:                make(chan struct{}),
		isConnect:           isConnect,
		connectAddr:         connectAddr,
		reserved:            reserved,
	}
}

// hasReserved reports whether any Cloudflare "reserved" value is configured
// (WARP). When none is set the bind must leave bytes 1-3 untouched, so a plain
// WireGuard / AmneziaWG endpoint keeps its magic header intact.
func (c *ClientBind) hasReserved() bool {
	if c.reserved != [3]uint8{} {
		return true
	}
	for _, reserved := range c.reservedForEndpoint {
		if reserved != [3]uint8{} {
			return true
		}
	}
	return false
}

func (c *ClientBind) connect() (*wireConn, error) {
	serverConn := c.conn.Load()
	if serverConn != nil {
		select {
		case <-serverConn.done:
			serverConn = nil
		default:
			return serverConn, nil
		}
	}
	c.connAccess.Lock()
	defer c.connAccess.Unlock()
	select {
	case <-c.done:
		return nil, net.ErrClosed
	default:
	}
	serverConn = c.conn.Load()
	if serverConn != nil {
		select {
		case <-serverConn.done:
			serverConn = nil
		default:
			return serverConn, nil
		}
	}
	if c.isConnect {
		udpConn, err := c.dialer.DialContext(c.bindCtx, N.NetworkUDP, M.SocksaddrFromNetIP(c.connectAddr))
		if err != nil {
			return nil, err
		}
		serverConn = &wireConn{
			PacketConn: bufio.NewUnbindPacketConn(udpConn),
			done:       make(chan struct{}),
		}
	} else {
		udpConn, err := c.dialer.ListenPacket(c.bindCtx, M.Socksaddr{Addr: netip.IPv4Unspecified()})
		if err != nil {
			return nil, err
		}
		serverConn = &wireConn{
			PacketConn: bufio.NewPacketConn(udpConn),
			done:       make(chan struct{}),
		}
	}
	c.conn.Store(serverConn)
	return serverConn, nil
}

func (c *ClientBind) Open(port uint16) (fns []conn.ReceiveFunc, actualPort uint16, err error) {
	select {
	case <-c.done:
		c.done = make(chan struct{})
	default:
	}
	c.bindCtx, c.bindDone = context.WithCancel(c.ctx)
	return []conn.ReceiveFunc{c.receive}, 0, nil
}

func (c *ClientBind) receive(packets [][]byte, sizes []int, eps []conn.Endpoint) (count int, err error) {
	udpConn, err := c.connect()
	if err != nil {
		select {
		case <-c.done:
			return
		default:
		}
		c.logger.Error(E.Cause(err, "connect to server"))
		err = nil
		c.pauseManager.WaitActive()
		time.Sleep(time.Second)
		return
	}
	n, addr, err := udpConn.ReadFrom(packets[0])
	if err != nil {
		udpConn.Close()
		select {
		case <-c.done:
		default:
			c.logger.Error(E.Cause(err, "read packet"))
			err = nil
		}
		return
	}
	sizes[0] = n
	// lx: only strip the Cloudflare "reserved" bytes when a reserved value is
	// actually configured (WARP). AmneziaWG writes a full uint32 magic header
	// into bytes 0-3; unconditionally clearing 1-3 (as upstream ClientBind did)
	// destroys ranged h1-h4 headers, so the AWG endpoint drops every packet.
	// StdNetBind (the no-detour path) never clears on receive either.
	if n > 3 && c.hasReserved() {
		b := packets[0]
		clear(b[1:4])
	}
	eps[0] = remoteEndpoint(M.SocksaddrFromNet(addr).Unwrap().AddrPort())
	count = 1
	return
}

func (c *ClientBind) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	if c.bindDone != nil {
		c.bindDone()
	}
	c.connAccess.Lock()
	defer c.connAccess.Unlock()
	common.Close(common.PtrOrNil(c.conn.Load()))
	return nil
}

func (c *ClientBind) SetMark(mark uint32) error {
	return nil
}

func (c *ClientBind) Send(bufs [][]byte, ep conn.Endpoint, offset int) error {
	udpConn, err := c.connect()
	if err != nil {
		c.pauseManager.WaitActive()
		time.Sleep(time.Second)
		return err
	}
	destination := netip.AddrPort(ep.(remoteEndpoint))
	for _, buf := range bufs {
		if offset > 0 {
			buf = buf[offset:]
		}
		if len(buf) > 3 {
			reserved, loaded := c.reservedForEndpoint[destination]
			if !loaded {
				reserved = c.reserved
			}
			// lx: only stamp the reserved bytes when non-zero (WARP). For a
			// plain WG / AmneziaWG endpoint reserved is [0,0,0]; overwriting
			// bytes 1-3 would zero the upper bytes of an AWG magic header and
			// break the tunnel. See the matching guard in receive().
			if reserved != [3]uint8{} {
				copy(buf[1:4], reserved[:])
			}
		}
		_, err = udpConn.WriteToUDPAddrPort(buf, destination)
		if err != nil {
			udpConn.Close()
			return err
		}
	}
	return nil
}

func (c *ClientBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return remoteEndpoint(ap), nil
}

func (c *ClientBind) BatchSize() int {
	return 1
}

func (c *ClientBind) SetReservedForEndpoint(destination netip.AddrPort, reserved [3]byte) {
	c.reservedForEndpoint[destination] = reserved
}

type wireConn struct {
	net.PacketConn
	conn   net.Conn
	access sync.Mutex
	done   chan struct{}
}

func (w *wireConn) WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	if w.conn != nil {
		return w.conn.Write(b)
	}
	return w.PacketConn.WriteTo(b, M.SocksaddrFromNetIP(addr).UDPAddr())
}

func (w *wireConn) Close() error {
	w.access.Lock()
	defer w.access.Unlock()
	select {
	case <-w.done:
		return net.ErrClosed
	default:
	}
	w.PacketConn.Close()
	close(w.done)
	return nil
}

var _ conn.Endpoint = (*remoteEndpoint)(nil)

type remoteEndpoint netip.AddrPort

func (e remoteEndpoint) ClearSrc() {
}

func (e remoteEndpoint) SrcToString() string {
	return ""
}

func (e remoteEndpoint) DstToString() string {
	return (netip.AddrPort)(e).String()
}

func (e remoteEndpoint) DstToBytes() []byte {
	b, _ := (netip.AddrPort)(e).MarshalBinary()
	return b
}

func (e remoteEndpoint) DstIP() netip.Addr {
	return (netip.AddrPort)(e).Addr()
}

func (e remoteEndpoint) SrcIP() netip.Addr {
	return netip.Addr{}
}
