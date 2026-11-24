//go:build with_gvisor

package wireguard

// lx: SPEC 021 — CoalescingWriter delivers a batch of tunnel packets to the
// userspace stack through GRO. End to end over TCP: the test plays the remote
// peer with raw segments and reads the stack's replies from its outbound
// queue.

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/checksum"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/sing-tun"
)

var (
	coalesceLocal  = netip.MustParseAddr("10.0.0.2")
	coalesceRemote = netip.MustParseAddr("10.0.0.1")
)

const coalescePort = 80

func TestNewCoalescingWriterNonStack(t *testing.T) {
	t.Parallel()
	if w := NewCoalescingWriter(nil); w != nil {
		t.Fatal("writer for a nil device")
	}
	if w := NewCoalescingWriter((*systemDevice)(nil)); w != nil {
		t.Fatal("writer for a system device")
	}
}

func tcpSegment(srcPort uint16, seq, ack uint32, flags header.TCPFlags, payload []byte) []byte {
	packet := make([]byte, header.IPv4MinimumSize+header.TCPMinimumSize+len(payload))
	ip := header.IPv4(packet)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(packet)),
		TTL:         64,
		Protocol:    uint8(header.TCPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(coalesceRemote.As4()),
		DstAddr:     tcpip.AddrFrom4(coalesceLocal.As4()),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	tcp := header.TCP(packet[header.IPv4MinimumSize:])
	tcp.Encode(&header.TCPFields{
		SrcPort:    srcPort,
		DstPort:    coalescePort,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: header.TCPMinimumSize,
		Flags:      flags,
		WindowSize: 65535,
	})
	copy(tcp.Payload(), payload)
	xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber, ip.SourceAddress(), ip.DestinationAddress(), uint16(len(tcp)))
	tcp.SetChecksum(^tcp.CalculateChecksum(checksum.Checksum(payload, xsum)))
	return packet
}

type coalesceFixture struct {
	t        *testing.T
	device   *stackDevice
	writer   *CoalescingWriter
	listener *gonet.TCPListener
}

func newCoalesceFixture(t *testing.T) *coalesceFixture {
	device := newCloseTestStackDevice(t)
	t.Cleanup(func() { device.Close() })
	writer := NewCoalescingWriter(device)
	if writer == nil {
		t.Fatal("no writer for a stack device")
	}
	listener, err := gonet.ListenTCP(device.stack, tcpip.FullAddress{
		NIC:  tun.DefaultNIC,
		Addr: tcpip.AddrFrom4(coalesceLocal.As4()),
		Port: coalescePort,
	}, header.IPv4ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return &coalesceFixture{t: t, device: device, writer: writer, listener: listener}
}

// handshake opens a connection from srcPort and returns the accepted conn and
// the next sequence numbers of both sides.
func (f *coalesceFixture) handshake(srcPort uint16) (net.Conn, uint32, uint32) {
	t := f.t
	t.Helper()
	const clientISN = 1000
	f.writer.Write([][]byte{tcpSegment(srcPort, clientISN, 0, header.TCPFlagSyn, nil)})
	var synAck header.TCP
	select {
	case packet := <-f.device.outbound:
		reply := header.IPv4(bytes.Clone(packet.ToView().AsSlice()))
		packet.DecRef()
		synAck = header.TCP(reply.Payload())
	case <-time.After(5 * time.Second):
		t.Fatal("no SYN-ACK")
	}
	if synAck.Flags() != header.TCPFlagSyn|header.TCPFlagAck || synAck.AckNumber() != clientISN+1 || synAck.DestinationPort() != srcPort {
		t.Fatalf("not a SYN-ACK for the SYN: flags %v ack %d port %d", synAck.Flags(), synAck.AckNumber(), synAck.DestinationPort())
	}
	serverNext := synAck.SequenceNumber() + 1
	f.writer.Write([][]byte{tcpSegment(srcPort, clientISN+1, serverNext, header.TCPFlagAck, nil)})
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := f.listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	select {
	case conn := <-accepted:
		t.Cleanup(func() { conn.Close() })
		return conn, clientISN + 1, serverNext
	case <-time.After(5 * time.Second):
		t.Fatal("no accepted connection")
	}
	return nil, 0, 0
}

func readAll(t *testing.T, conn net.Conn, size int) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data := make([]byte, size)
	if _, err := io.ReadFull(conn, data); err != nil {
		t.Fatalf("read %d bytes: %v", size, err)
	}
	return data
}

// Consecutive segments of one connection in a batch reach the socket intact
// and enter TCP as fewer segments than were sent.
func TestCoalescingWriterTCP(t *testing.T) {
	t.Parallel()
	f := newCoalesceFixture(t)
	conn, seq, ack := f.handshake(40000)
	const segments, segmentSize = 8, 1000
	want := make([]byte, segments*segmentSize)
	for i := range want {
		want[i] = byte(i * 7)
	}
	batch := make([][]byte, segments)
	for i := range batch {
		batch[i] = tcpSegment(40000, seq+uint32(i*segmentSize), ack, header.TCPFlagAck, want[i*segmentSize:(i+1)*segmentSize])
	}
	before := f.device.stack.Stats().TCP.ValidSegmentsReceived.Value()
	f.writer.Write(batch)
	received := f.device.stack.Stats().TCP.ValidSegmentsReceived.Value() - before
	if got := readAll(t, conn, len(want)); !bytes.Equal(got, want) {
		t.Fatal("data differs from what was sent")
	}
	if received >= segments {
		t.Fatalf("stack received %d segments for %d sent, nothing coalesced", received, segments)
	}
}

// Two connections that share a GRO bucket: one Write delivers both, although
// a single gro.Flush releases only the first packet of a bucket.
func TestCoalescingWriterTCPSharedBucket(t *testing.T) {
	t.Parallel()
	f := newCoalesceFixture(t)
	// The bucket is a sum of address bytes and ports, masked to 8 buckets.
	connA, seqA, ackA := f.handshake(40000)
	connB, seqB, ackB := f.handshake(40008)
	dataA, dataB := bytes.Repeat([]byte{0xa1}, 500), bytes.Repeat([]byte{0xb2}, 700)
	f.writer.Write([][]byte{
		tcpSegment(40000, seqA, ackA, header.TCPFlagAck, dataA),
		tcpSegment(40008, seqB, ackB, header.TCPFlagAck, dataB),
	})
	if got := readAll(t, connA, len(dataA)); !bytes.Equal(got, dataA) {
		t.Fatal("connection A: data differs")
	}
	if got := readAll(t, connB, len(dataB)); !bytes.Equal(got, dataB) {
		t.Fatal("connection B: data differs")
	}
}
