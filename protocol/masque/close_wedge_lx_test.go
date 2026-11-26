package masque

// lx: SPEC 146 — Close must not stand behind a tunnel dial. Field dump
// 2026-11-01: Box.Close waited on runMu held by ensureSession, whose h3 dial
// sat in quic.DialEarly forever: through a detour the PacketConn's
// SetReadDeadline was a no-op, so quic-go's Transport.Close never woke its
// listener and the dial never returned.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/transport/masque"
	tun "github.com/sagernet/sing-tun"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// noDeadlineConn mimics a stream-tunnelled UDP conn behind a detour (vless
// XUDP over WebSocket): deadlines are not supported and the conn says it needs
// an additional read deadline.
type noDeadlineConn struct {
	net.Conn
}

func (c *noDeadlineConn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *noDeadlineConn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *noDeadlineConn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }
func (c *noDeadlineConn) NeedAdditionalReadDeadline() bool { return true }

// pipeDialer hands out the client end of a net.Pipe as the "UDP socket".
type pipeDialer struct {
	conn net.Conn
}

func (d *pipeDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

func (d *pipeDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

var _ N.Dialer = (*pipeDialer)(nil)

func TestH3DialReturnsWhenDetourConnLacksReadDeadline(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	// Swallow whatever quic-go sends, so its writes never block on the pipe.
	go func() { _, _ = io.Copy(io.Discard, server) }()

	o := &Outbound{
		logger: discardLogger{},
		dialer: &pipeDialer{conn: &noDeadlineConn{Conn: client}},
		server: M.ParseSocksaddr("127.0.0.1:443"),
		tlsConfig: &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h3"},
			ServerName:         "example.com",
		},
		quicConfig: &quic.Config{},
	}

	// Nobody answers: the handshake can only end by the context.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := o.connectH3WithBudget(ctx, 0)
		done <- err
	}()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("connectH3WithBudget must fail: nobody answered the handshake")
		}
	case <-timer.C:
		t.Fatal("connectH3WithBudget did not return: detour conn without read deadline wedged quic-go Transport.Close")
	}
}

// requireUserspaceStack skips when the build carries no gVisor: ensureSession
// builds a userspace stack before it dials.
func requireUserspaceStack(t *testing.T) {
	t.Helper()
	if !tun.WithGVisor {
		t.Skip("userspace stack needs with_gvisor")
	}
}

func newCloseTestOutbound(legs *connectLegs) *Outbound {
	return &Outbound{
		ctx:         context.Background(),
		logger:      discardLogger{},
		network:     "h3",
		mtu:         1280,
		prefixes:    []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")},
		server:      M.ParseSocksaddr("127.0.0.1:443"),
		tlsConfig:   &tls.Config{ServerName: "example.com"},
		legsForTest: legs,
	}
}

func failH2(context.Context) (io.Closer, masque.IpConn, error) {
	return nil, nil, errors.New("h2 must not be dialled")
}

func readSession(o *Outbound) *session {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	return o.sess
}

func TestCloseDoesNotWaitForDialInFlight(t *testing.T) {
	t.Parallel()
	requireUserspaceStack(t)
	entered := make(chan struct{})
	o := newCloseTestOutbound(&connectLegs{
		h3: func(ctx context.Context, _ time.Duration) (io.Closer, masque.IpConn, error) {
			close(entered)
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
		h2: failH2,
	})

	result := make(chan error, 1)
	go func() {
		_, err := o.ensureSession(context.Background())
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSession never reached the h3 leg")
	}

	closed := make(chan struct{})
	go func() {
		_ = o.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close waited for the dial in flight: it must not take the lock held across the dial")
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("ensureSession must fail once Close cancelled the dial")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureSession did not return after Close cancelled the dial")
	}
	if readSession(o) != nil {
		t.Fatal("no session may be current after Close")
	}
	if err := o.Close(); err != nil {
		t.Fatal("second Close must be a no-op: ", err)
	}
}

// countingCloser records Close calls.
type countingCloser struct {
	closed atomic.Int32
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)
	return nil
}

// blockingIpConn is a tunnel that never carries a packet; reads wait for Close.
type blockingIpConn struct {
	once sync.Once
	done chan struct{}
}

func newBlockingIpConn() *blockingIpConn {
	return &blockingIpConn{done: make(chan struct{})}
}

func (c *blockingIpConn) ReadPacket() ([]byte, error) {
	<-c.done
	return nil, net.ErrClosed
}

func (c *blockingIpConn) WritePacket([]byte) ([]byte, error) {
	return nil, nil
}

func (c *blockingIpConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func TestSessionBuiltAfterCloseIsTornDown(t *testing.T) {
	t.Parallel()
	requireUserspaceStack(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	closer := &countingCloser{}
	ipConn := newBlockingIpConn()
	o := newCloseTestOutbound(&connectLegs{
		// The leg ignores cancellation: the tunnel comes up after Close.
		h3: func(context.Context, time.Duration) (io.Closer, masque.IpConn, error) {
			close(entered)
			<-release
			return closer, ipConn, nil
		},
		h2: failH2,
	})

	result := make(chan error, 1)
	go func() {
		_, err := o.ensureSession(context.Background())
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSession never reached the h3 leg")
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)

	select {
	case err := <-result:
		if !errors.Is(err, errOutboundClosed) {
			t.Fatalf("a session built after Close must be refused with errOutboundClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureSession did not return")
	}
	if closer.closed.Load() == 0 {
		t.Fatal("the tunnel built after Close must be torn down")
	}
	select {
	case <-ipConn.done:
	default:
		t.Fatal("the tunnel's ip conn must be closed")
	}
	if readSession(o) != nil {
		t.Fatal("no session may be current after Close")
	}
}
