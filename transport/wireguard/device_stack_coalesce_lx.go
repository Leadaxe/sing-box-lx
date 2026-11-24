//go:build with_gvisor

package wireguard

import (
	"github.com/sagernet/gvisor/pkg/buffer"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/stack/gro"
)

// lx: receive coalescing in front of the userspace stack (SPEC 021).
//
// The stack device delivers a batch one packet at a time, so TCP handles and
// acknowledges every 1280-byte segment on its own. A caller that already holds
// a group of packets of one tunnel hands it to CoalescingWriter instead:
// gVisor's GRO merges consecutive segments of a connection into one before the
// stack sees them. It is a separate writer rather than the device's own Write
// because WireGuard feeds the same device and is left as upstream has it.

// groBucketSize is the capacity of a GRO bucket in gVisor (gro.groBucketSize).
const groBucketSize = 8

// CoalescingWriter delivers batches of inbound IP packets to a stack device
// through GRO. It is used by one goroutine.
type CoalescingWriter struct {
	gro gro.GRO
}

// NewCoalescingWriter returns a writer for device, or nil when the device is
// not the userspace stack.
func NewCoalescingWriter(device Device) *CoalescingWriter {
	stackDevice, isStack := device.(*stackDevice)
	if !isStack || stackDevice.dispatcher == nil {
		return nil
	}
	w := &CoalescingWriter{}
	w.gro.Dispatcher = stackDevice.dispatcher
	w.gro.Init(true)
	return w
}

// Write delivers packets, merged where GRO can merge them, and flushes.
func (w *CoalescingWriter) Write(packets [][]byte) {
	for _, packet := range packets {
		if len(packet) == 0 {
			continue
		}
		packetBuffer := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(packet),
		})
		switch header.IPVersion(packet) {
		case header.IPv4Version:
			packetBuffer.NetworkProtocolNumber = header.IPv4ProtocolNumber
		case header.IPv6Version:
			packetBuffer.NetworkProtocolNumber = header.IPv6ProtocolNumber
		}
		// The device declares CapabilityRXChecksumOffload: the tunnel carried
		// the packet under its own integrity check.
		packetBuffer.RXChecksumValidated = true
		w.gro.Enqueue(packetBuffer)
		packetBuffer.DecRef()
	}
	// gro.Flush releases one packet per bucket and call: it walks the bucket
	// list past the entry it has just unlinked. A bucket holds one packet per
	// connection, so repeat it for a full bucket; nothing may stay behind
	// until the next batch.
	for range groBucketSize {
		w.gro.Flush()
	}
}
