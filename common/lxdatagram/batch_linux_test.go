//go:build linux

package lxdatagram

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

// newBatchPair returns a peer socket and a batched conn connected to it.
func newBatchPair(t *testing.T, network string, address string) (*net.UDPConn, *net.UDPConn, net.Conn) {
	t.Helper()
	peer, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP(address)})
	if err != nil {
		t.Skipf("listen %s %s: %v", network, address, err)
	}
	t.Cleanup(func() { peer.Close() })
	udpConn, err := net.DialUDP(network, nil, peer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udpConn.Close() })
	conn := BatchReads(udpConn)
	if conn == net.Conn(udpConn) {
		t.Fatal("BatchReads returned a kernel UDP conn unchanged")
	}
	return peer, udpConn, conn
}

func batchDatagram(index int) []byte {
	b := make([]byte, 20+index*13)
	for i := range b {
		b[i] = byte(index + i)
	}
	return b
}

func sendTo(t *testing.T, peer *net.UDPConn, to net.Addr, b []byte) {
	t.Helper()
	if _, err := peer.WriteTo(b, to); err != nil {
		t.Fatal(err)
	}
}

func readBatchSequence(t *testing.T, network string, address string) {
	t.Helper()
	peer, udpConn, conn := newBatchPair(t, network, address)
	const count = 100
	for i := range count {
		sendTo(t, peer, udpConn.LocalAddr(), batchDatagram(i))
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 2048)
	for i := range count {
		n, err := conn.Read(buffer)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if want := batchDatagram(i); !bytes.Equal(buffer[:n], want) {
			t.Fatalf("datagram %d: got %d bytes, want %d bytes of its own content", i, n, len(want))
		}
	}
}

func TestBatchReadsSequenceIPv4(t *testing.T) {
	t.Parallel()
	readBatchSequence(t, "udp4", "127.0.0.1")
}

func TestBatchReadsSequenceIPv6(t *testing.T) {
	t.Parallel()
	readBatchSequence(t, "udp6", "::1")
}

// A datagram longer than the read buffer is cut to it, as a plain read cuts
// it, and the next datagram arrives whole.
func TestBatchReadsTruncatesToBuffer(t *testing.T) {
	t.Parallel()
	peer, udpConn, conn := newBatchPair(t, "udp4", "127.0.0.1")
	long, next := batchDatagram(70), batchDatagram(3)
	sendTo(t, peer, udpConn.LocalAddr(), long)
	sendTo(t, peer, udpConn.LocalAddr(), next)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	short := make([]byte, 100)
	n, err := conn.Read(short)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(short) || !bytes.Equal(short, long[:len(short)]) {
		t.Fatalf("oversize datagram: got %d bytes, want its first %d", n, len(short))
	}
	buffer := make([]byte, 2048)
	n, err = conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], next) {
		t.Fatalf("next datagram: got %d bytes, want %d intact", n, len(next))
	}
}

func TestBatchReadsDeadline(t *testing.T) {
	t.Parallel()
	_, _, conn := newBatchPair(t, "udp4", "127.0.0.1")
	buffer := make([]byte, 2048)
	for _, deadline := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(50 * time.Millisecond)} {
		conn.SetReadDeadline(deadline)
		_, err := conn.Read(buffer)
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("read past deadline: %v, want a timeout", err)
		}
	}
}

// After a burst and an idle period, a lone datagram is not held back.
func TestBatchReadsLoneDatagramAfterIdle(t *testing.T) {
	t.Parallel()
	peer, udpConn, conn := newBatchPair(t, "udp4", "127.0.0.1")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 2048)
	for i := range 10 {
		sendTo(t, peer, udpConn.LocalAddr(), batchDatagram(i))
	}
	for i := range 10 {
		if _, err := conn.Read(buffer); err != nil {
			t.Fatalf("burst read %d: %v", i, err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	lone := batchDatagram(42)
	start := time.Now()
	sendTo(t, peer, udpConn.LocalAddr(), lone)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("lone datagram took %v", took)
	}
	if !bytes.Equal(buffer[:n], lone) {
		t.Fatalf("lone datagram: got %d bytes, want %d", n, len(lone))
	}
}

func TestBatchReadsUpstream(t *testing.T) {
	t.Parallel()
	_, udpConn, conn := newBatchPair(t, "udp4", "127.0.0.1")
	upstream, isUpstream := conn.(interface{ Upstream() any })
	if !isUpstream {
		t.Fatalf("%T has no Upstream", conn)
	}
	if upstream.Upstream() != any(udpConn) {
		t.Fatalf("Upstream = %T, want the dialled conn", upstream.Upstream())
	}
}
