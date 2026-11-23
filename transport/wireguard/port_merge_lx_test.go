package wireguard

import (
	"testing"

	"github.com/sagernet/sing-tun"
)

// The shape wireguard-go type-asserts its TUN device to (device.writeMerger).
var _ interface{ MergesWrites() bool } = (*returnDeviceWrapper)(nil)

type headroomReturn int

func (r headroomReturn) ReturnHeadroom() int                     { return int(r) }
func (r headroomReturn) ReturnPackets(packets [][]byte) [][]byte { return packets }

// TestReturnDeviceMergesWrites: only a return path that asks for front
// headroom beyond the packet offset — a Linux TUN with the virtio header —
// makes the device report that its writes are merged.
func TestReturnDeviceMergesWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attached bool
		headroom int
		want     bool
	}{
		{"no return path", false, 0, false},
		{"gVisor writeback or ICMP forwarder", true, 0, false},
		{"system TUN without GSO", true, tun.PacketOffset, false},
		{"Linux TUN with the virtio header", true, tun.PacketOffset + 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := &Endpoint{returnDevice: &returnDeviceWrapper{}}
			if tc.attached {
				if err := endpoint.AttachReturn(headroomReturn(tc.headroom)); err != nil {
					t.Fatal(err)
				}
			}
			if got := endpoint.returnDevice.MergesWrites(); got != tc.want {
				t.Fatalf("MergesWrites() = %v, want %v", got, tc.want)
			}
		})
	}
}
