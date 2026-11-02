package lxdatagram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing/common/bufio/deadline"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// framedConn mimics a datagram conn carried in a stream (vless.PacketConn):
// every Read hands out one whole datagram and a buffer shorter than the
// datagram is answered with io.ErrShortBuffer, never with a part of it.
type framedConn struct {
	queue  chan []byte
	closed chan struct{}
}

func newFramedConn(datagrams ...[]byte) *framedConn {
	c := &framedConn{queue: make(chan []byte, len(datagrams)+8), closed: make(chan struct{})}
	for _, datagram := range datagrams {
		c.queue <- datagram
	}
	return c
}

func (c *framedConn) Read(p []byte) (int, error) {
	select {
	case datagram := <-c.queue:
		if len(p) < len(datagram) {
			return 0, io.ErrShortBuffer
		}
		return copy(p, datagram), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *framedConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *framedConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
func (c *framedConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *framedConn) RemoteAddr() net.Addr             { return &net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 443} }
func (c *framedConn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *framedConn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *framedConn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }
func (c *framedConn) NeedAdditionalReadDeadline() bool { return true }

func datagram(size int, fill byte) []byte {
	return bytes.Repeat([]byte{fill}, size)
}

func TestTruncateReadsCutsOversizeDatagramAndKeepsBoundaries(t *testing.T) {
	framed := newFramedConn(datagram(1462, 0xa1), datagram(100, 0xb2), datagram(1452, 0xc3), datagram(10, 0xd4))
	conn := TruncateReads(framed, nil)
	if conn == net.Conn(framed) {
		t.Fatal("a stream-carried conn must be wrapped")
	}
	p := make([]byte, 1452)

	n, err := conn.Read(p)
	if err != nil || n != 1452 {
		t.Fatalf("oversize datagram: got n=%d err=%v, want 1452 <nil>", n, err)
	}
	if !bytes.Equal(p, datagram(1452, 0xa1)) {
		t.Fatal("oversize datagram: the head of the datagram must be delivered")
	}
	n, err = conn.Read(p)
	if err != nil || n != 100 || !bytes.Equal(p[:n], datagram(100, 0xb2)) {
		t.Fatalf("after truncation the next datagram must follow, not the tail: n=%d err=%v", n, err)
	}
	n, err = conn.Read(p)
	if err != nil || n != 1452 || !bytes.Equal(p, datagram(1452, 0xc3)) {
		t.Fatalf("exact-size datagram: n=%d err=%v", n, err)
	}
	n, err = conn.Read(p)
	if err != nil || n != 10 || !bytes.Equal(p[:n], datagram(10, 0xd4)) {
		t.Fatalf("short datagram: n=%d err=%v", n, err)
	}
}

func TestTruncateReadsPassesErrorsThrough(t *testing.T) {
	framed := newFramedConn()
	conn := TruncateReads(framed, nil)
	_ = framed.Close()
	_, err := conn.Read(make([]byte, 1452))
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed conn: got %v, want net.ErrClosed", err)
	}
}

// replaceableConn mimics dialer.udpConn: a replaceable reader over a kernel
// socket.
type replaceableConn struct {
	net.Conn
}

func (c *replaceableConn) Upstream() any           { return c.Conn }
func (c *replaceableConn) ReaderReplaceable() bool { return true }

func TestTruncateReadsLeavesKernelSocketAlone(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	defer udpConn.Close()
	if TruncateReads(udpConn, nil) != net.Conn(udpConn) {
		t.Fatal("*net.UDPConn must pass through unwrapped")
	}
	wrapped := &replaceableConn{Conn: udpConn}
	if TruncateReads(wrapped, nil) != net.Conn(wrapped) {
		t.Fatal("a replaceable reader over a kernel socket must pass through unwrapped")
	}
}

type recordingDialer struct {
	conn net.Conn
}

func (d *recordingDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

func (d *recordingDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

func TestDialerWrapsOnlyUDP(t *testing.T) {
	framed := newFramedConn(datagram(1462, 0xa1))
	dialer := Dialer(context.Background(), &recordingDialer{conn: framed}, nil)
	destination := M.ParseSocksaddr("1.1.1.1:443")

	tcpConn, err := dialer.DialContext(context.Background(), N.NetworkTCP, destination)
	if err != nil || tcpConn != net.Conn(framed) {
		t.Fatalf("tcp must pass through: conn=%T err=%v", tcpConn, err)
	}
	udpConn, err := dialer.DialContext(context.Background(), N.NetworkUDP, destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, isTrunc := udpConn.(*truncConn); !isTrunc {
		t.Fatalf("udp must be wrapped, got %T", udpConn)
	}
	n, err := udpConn.Read(make([]byte, 1452))
	if err != nil || n != 1452 {
		t.Fatalf("udp through the dialer must truncate: n=%d err=%v", n, err)
	}
	if _, err = dialer.ListenPacket(context.Background(), destination); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("ListenPacket must pass through: %v", err)
	}
}

// The SPEC 146 deadline wrapper reads into a buffer of the caller's length and
// passes a short-buffer error up unchanged; the truncating layer reads with its
// own full-size buffer, so it keeps the datagram whole on either side of the
// deadline wrapper. Both orders are pinned.
func TestTruncateComposesWithDeadlineWrapperInEitherOrder(t *testing.T) {
	framed := newFramedConn(datagram(1462, 0xa1))
	conn := deadline.NewConn(TruncateReads(framed, nil))
	defer conn.Close()
	n, err := conn.Read(make([]byte, 1452))
	if err != nil || n != 1452 {
		t.Fatalf("deadline over truncate: n=%d err=%v, want 1452 <nil>", n, err)
	}

	reversed := newFramedConn(datagram(1462, 0xa1))
	outer := TruncateReads(deadline.NewConn(reversed), nil)
	defer outer.Close()
	n, err = outer.Read(make([]byte, 1452))
	if err != nil || n != 1452 {
		t.Fatalf("truncate over deadline: n=%d err=%v, want 1452 <nil>", n, err)
	}

	// Without the truncating layer the deadline wrapper alone reproduces the
	// field failure: the short buffer is fatal.
	bare := deadline.NewConn(newFramedConn(datagram(1462, 0xa1)))
	defer bare.Close()
	_, err = bare.Read(make([]byte, 1452))
	if !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("deadline wrapper alone: got %v, want io.ErrShortBuffer", err)
	}
}

func TestTruncateReadsIsTransparentToDeadlineCheck(t *testing.T) {
	framed := newFramedConn()
	defer framed.Close()
	if !deadline.NeedAdditionalReadDeadline(TruncateReads(framed, nil)) {
		t.Fatal("NeedAdditionalReadDeadline must be seen through the wrapper")
	}
}
