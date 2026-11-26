package masque

// lx: SPEC 150 — an oversize datagram from the server through a stream detour
// must be dropped, not kill the QUIC transport. Stand 2026-11-01: Cloudflare
// WARP sends PMTUD probes of 1453–1462 bytes over the 1452 quic-go advertises;
// vless.PacketConn / XUDP answer the short read buffer with io.ErrShortBuffer
// and quic-go closed the transport on it.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
	M "github.com/sagernet/sing/common/metadata"
)

// framedDetourConn mimics a UDP conn tunnelled through a stream detour: each
// datagram is handed out whole and a buffer shorter than the datagram gets
// io.ErrShortBuffer (vless.PacketConn.Read, XUDPConn.ReadPacket). Deadlines
// are not supported, like XUDP over WebSocket.
type framedDetourConn struct {
	queue  chan []byte
	closed chan struct{}
}

func (c *framedDetourConn) Read(p []byte) (int, error) {
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

func (c *framedDetourConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *framedDetourConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
func (c *framedDetourConn) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *framedDetourConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(162, 159, 198, 2), Port: 443}
}
func (c *framedDetourConn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *framedDetourConn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *framedDetourConn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }
func (c *framedDetourConn) NeedAdditionalReadDeadline() bool { return true }

func TestH3DialSurvivesOversizeDatagramThroughDetour(t *testing.T) {
	t.Parallel()
	// The first "server" datagram is a 1462-byte 1-RTT-looking packet: ten
	// bytes over quic-go's 1452-byte read buffer, the largest PMTUD probe seen
	// from WARP. Nobody ever answers the handshake itself.
	probe := make([]byte, 1462)
	probe[0] = 0x40
	conn := &framedDetourConn{queue: make(chan []byte, 1), closed: make(chan struct{})}
	conn.queue <- probe
	t.Cleanup(func() { _ = conn.Close() })

	o := &Outbound{
		logger: discardLogger{},
		dialer: &pipeDialer{conn: conn},
		server: M.ParseSocksaddr("162.159.198.2:443"),
		tlsConfig: &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h3"},
			ServerName:         "example.com",
		},
		quicConfig: &quic.Config{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := o.connectH3WithBudget(ctx, 0)
		done <- err
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		t.Fatal("connectH3WithBudget did not return")
	}
	if err == nil {
		t.Fatal("connectH3WithBudget must fail: nobody answered the handshake")
	}
	if errors.Is(err, io.ErrShortBuffer) || strings.Contains(err.Error(), "short buffer") {
		t.Fatalf("the oversize datagram killed the transport: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the dial must end by the context, not by the datagram: %v", err)
	}
}
