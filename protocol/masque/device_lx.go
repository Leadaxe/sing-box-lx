package masque

// lx: the userspace stack of the masque outbound on the 1.15 line.
//
// On 1.14 the outbound took the WireGuard stack device and moved packets
// through its Read and Write. The 1.15 WireGuard stack device hands every
// packet the stack sends to its WireGuard peers and never returns one from
// Read, so it cannot carry a tunnel of its own. The outbound uses upstream's
// generic stack device instead (transport/device, the one masque-client runs
// on) behind the interface the outbound already speaks: the packets the stack
// sends are queued for Read, and Write feeds the stack.

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/transport/device"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	wgDevice "github.com/sagernet/wireguard-go/device"
	wgTun "github.com/sagernet/wireguard-go/tun"
)

// tunnelDeviceBatchSize is how many packets one Read returns at most.
const tunnelDeviceBatchSize = 64

type tunnelDevice struct {
	device.Device
	mtu          uint32
	inet4Address netip.Addr
	inet6Address netip.Addr
	outbound     chan []*buf.Buffer
	pending      []*buf.Buffer
	closed       chan struct{}
	closeOnce    sync.Once
}

var _ wireguard.Device = (*tunnelDevice)(nil)

func newTunnelDevice(options wireguard.DeviceOptions) (wireguard.Device, error) {
	stackDevice, err := device.New(device.Options{
		Context: options.Context,
		Logger:  options.Logger,
		MTU:     options.MTU,
		Configuration: device.Configuration{
			MTU:     options.MTU,
			Address: options.Address,
		},
	})
	if err != nil {
		return nil, err
	}
	d := &tunnelDevice{
		Device:   stackDevice,
		mtu:      options.MTU,
		outbound: make(chan []*buf.Buffer, tunnelDeviceBatchSize),
		closed:   make(chan struct{}),
	}
	d.inet4Address, d.inet6Address = stackDevice.PortAddresses()
	stackDevice.SetPacketWriter(d.writeOutbound)
	return d, nil
}

// writeOutbound takes the packets the stack sends: the buffers become ours, the
// slice holding them stays the caller's, so it is copied. It blocks while the
// tunnel pump is behind, which holds the stack back instead of dropping.
func (d *tunnelDevice) writeOutbound(packetBuffers []*buf.Buffer) error {
	select {
	case d.outbound <- append([]*buf.Buffer(nil), packetBuffers...):
		return nil
	case <-d.closed:
		buf.ReleaseMulti(packetBuffers)
		return os.ErrClosed
	}
}

// Read returns the packets the stack sent. One goroutine reads.
func (d *tunnelDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(d.pending) == 0 {
		select {
		case d.pending = <-d.outbound:
		case <-d.closed:
			return 0, os.ErrClosed
		}
	}
	var count int
	for count < len(bufs) && len(d.pending) > 0 {
		packetBuffer := d.pending[0]
		d.pending[0] = nil
		d.pending = d.pending[1:]
		sizes[count] = copy(bufs[count][offset:], packetBuffer.Bytes())
		packetBuffer.Release()
		count++
	}
	return count, nil
}

// Write hands the packets that came out of the tunnel to the stack.
func (d *tunnelDevice) Write(bufs [][]byte, offset int) (int, error) {
	packetBuffers := common.Map(bufs, func(packet []byte) *buf.Buffer {
		return buf.As(packet[offset:])
	})
	err := d.Device.WriteInboundBuffers(packetBuffers)
	if err != nil {
		return 0, err
	}
	return len(bufs), nil
}

// DialContext bounds the connect of a TCP dial as the WireGuard stack device
// does (SPEC 052, transport/wireguard/connect_deadline_lx.go): the stack's own
// SYN retries are the only other bound, and a silent tunnel would hold the
// dial for minutes.
func (d *tunnelDevice) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) == N.NetworkTCP {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, C.TCPTimeout)
		defer cancel()
	}
	return d.Device.DialContext(ctx, network, destination)
}

// Close leaves the batch a reader holds to that reader: Read returns
// os.ErrClosed from then on, and the garbage collector takes the buffers.
func (d *tunnelDevice) Close() error {
	d.closeOnce.Do(func() {
		close(d.closed)
	})
	err := d.Device.Close()
	for {
		select {
		case packetBuffers := <-d.outbound:
			buf.ReleaseMulti(packetBuffers)
		default:
			return err
		}
	}
}

func (d *tunnelDevice) File() *os.File { return nil }

func (d *tunnelDevice) Flush() error { return nil }

func (d *tunnelDevice) MTU() (int, error) { return int(d.mtu), nil }

func (d *tunnelDevice) Name() (string, error) { return "sing-box", nil }

func (d *tunnelDevice) Events() <-chan wgTun.Event { return nil }

func (d *tunnelDevice) BatchSize() int { return tunnelDeviceBatchSize }

func (d *tunnelDevice) SetDevice(*wgDevice.Device, []*wgDevice.Peer) {}

func (d *tunnelDevice) Inet4Address() netip.Addr { return d.inet4Address }

func (d *tunnelDevice) Inet6Address() netip.Addr { return d.inet6Address }
