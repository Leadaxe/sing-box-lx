package wireguard

import (
	"github.com/sagernet/sing-tun"
)

// MergesWrites answers wireguard-go's question whether packets written to this
// device may be coalesced in place before they leave (lx SPEC 158).
//
// wireguard-go queues an inbound packet in a buffer of its own size instead of
// a 64 KiB read slot, unless the TUN it writes to merges packets: a merge
// needs the capacity behind the first packet. It judges that by the batch size
// the device reports, and this device reports one packet per write. With a
// return path attached that is no longer the whole story: the packets go to
// the tun inbound's writeback instead, and on a Linux TUN with the virtio
// header that writeback coalesces TCP and UDP segments in place.
//
// Front headroom is how that case is recognized. Of sing-tun's writebacks only
// the Linux one over a GSO TUN asks for room in front of the packet beyond
// tun.PacketOffset (the virtio header); the gVisor writeback and the ICMP
// forwarder's return path ask for none and copy what they are given.
func (d *returnDeviceWrapper) MergesWrites() bool {
	state := d.state.Load()
	return state != nil && state.headroom > tun.PacketOffset
}
