package masque

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/transport/wireguard"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// The tunnel device carries packets both ways: what a dial through the stack
// sends comes out of Read, and a packet given to Write reaches that dial.
func TestTunnelDeviceCarriesPacketsBothWays(t *testing.T) {
	local := netip.MustParseAddr("172.16.0.2")
	stack, err := newTunnelDevice(wireguard.DeviceOptions{
		Context: context.Background(),
		Logger:  log.NewNOPFactory().NewLogger("masque"),
		MTU:     1280,
		Address: []netip.Prefix{netip.PrefixFrom(local, 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	device := stack.(*tunnelDevice)
	defer device.Close()
	if err = device.Start(); err != nil {
		t.Fatal(err)
	}
	if device.Inet4Address() != local {
		t.Fatalf("inet4 address %s, want %s", device.Inet4Address(), local)
	}

	remote := M.ParseSocksaddrHostPort("198.51.100.7", 53)
	conn, err := device.DialContext(context.Background(), N.NetworkUDP, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}

	bufs := make([][]byte, device.BatchSize())
	for i := range bufs {
		bufs[i] = make([]byte, 1500)
	}
	sizes := make([]int, len(bufs))
	readDone := make(chan int, 1)
	go func() {
		count, _ := device.Read(bufs, sizes, 0)
		readDone <- count
	}()
	var packet []byte
	select {
	case count := <-readDone:
		if count < 1 {
			t.Fatal("Read returned no packet")
		}
		packet = bufs[0][:sizes[0]]
	case <-time.After(5 * time.Second):
		t.Fatal("the stack sent nothing to Read")
	}
	if packet[0]>>4 != 4 || packet[9] != 17 {
		t.Fatalf("not an IPv4 UDP packet: % x", packet[:20])
	}
	headerLength := int(packet[0]&0x0f) * 4
	if netip.AddrFrom4([4]byte(packet[12:16])) != local || netip.AddrFrom4([4]byte(packet[16:20])) != remote.Addr {
		t.Fatalf("addresses %s -> %s", netip.AddrFrom4([4]byte(packet[12:16])), netip.AddrFrom4([4]byte(packet[16:20])))
	}
	localPort := binary.BigEndian.Uint16(packet[headerLength:])
	if !bytes.Equal(packet[headerLength+8:], []byte("ping")) {
		t.Fatalf("payload %q", packet[headerLength+8:])
	}

	reply := udpPacket(remote.Addr, local, remote.Port, localPort, []byte("pong"))
	if _, err = device.Write([][]byte{reply}, 0); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	response := make([]byte, 64)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatal("the reply did not reach the dial: ", err)
	}
	if string(response[:n]) != "pong" {
		t.Fatalf("reply %q", response[:n])
	}
}

func udpPacket(source, destination netip.Addr, sourcePort, destinationPort uint16, payload []byte) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], source.AsSlice())
	copy(packet[16:20], destination.AsSlice())
	binary.BigEndian.PutUint16(packet[10:], ^onesComplementSum(packet[:20]))
	binary.BigEndian.PutUint16(packet[20:], sourcePort)
	binary.BigEndian.PutUint16(packet[22:], destinationPort)
	binary.BigEndian.PutUint16(packet[24:], uint16(8+len(payload)))
	copy(packet[28:], payload)
	return packet
}

func onesComplementSum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i:]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return uint16(sum)
}

// Close while a reader waits in Read: the reader gets os.ErrClosed, under -race.
func TestTunnelDeviceCloseDuringRead(t *testing.T) {
	device, err := newTunnelDevice(wireguard.DeviceOptions{
		Context: context.Background(),
		Logger:  log.NewNOPFactory().NewLogger("masque"),
		MTU:     1280,
		Address: []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = device.Start(); err != nil {
		t.Fatal(err)
	}
	readErr := make(chan error, 1)
	go func() {
		bufs := [][]byte{make([]byte, 1500)}
		_, err := device.Read(bufs, make([]int, 1), 0)
		readErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = device.Close()
	select {
	case err = <-readErr:
		if err == nil {
			t.Fatal("Read after Close returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after Close")
	}
}
