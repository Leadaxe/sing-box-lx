// lx: SPEC 071 — the detour dial in ClientBind.connect() must be bounded.
// It runs while holding connAccess; unbounded, a half-alive detour node parks
// it forever (field dump: 54 minutes in an unread XHTTP upload pipe) and
// everything queued behind the mutex — sends, the bind's own Close, rebinds —
// starves with it. The blocking dialer below is the ctx-aware stand-in for
// "the SPEC 050 watchdog breaks the pipe when the dial context fires".
package wireguard

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

// blockingDialer blocks every dial until its context fires — the shape of a
// dead detour whose transport honours dial-context cancellation (SPEC 050).
type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// captureDialer records the dial context and returns a live UDP conn.
type captureDialer struct {
	dialCtx context.Context
}

func (d *captureDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.dialCtx = ctx
	return net.Dial("udp", "127.0.0.1:9")
}

func (d *captureDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	d.dialCtx = ctx
	return net.ListenPacket("udp", "127.0.0.1:0")
}

// A dead dial must return within the bound and release connAccess. Red on the
// pre-fix base: connect() never returns and the select times out.
func TestConnectDialBounded(t *testing.T) {
	oldTimeout := clientBindDialTimeout
	clientBindDialTimeout = 200 * time.Millisecond
	defer func() { clientBindDialTimeout = oldTimeout }()

	bind := NewClientBind(context.Background(), logger.NOP(), blockingDialer{}, true, netip.MustParseAddrPort("127.0.0.1:443"), [3]uint8{})
	_, _, err := bind.Open(0)
	if err != nil {
		t.Fatal(err)
	}

	connectDone := make(chan error, 1)
	go func() {
		_, connectErr := bind.connect()
		connectDone <- connectErr
	}()
	select {
	case connectErr := <-connectDone:
		if connectErr == nil {
			t.Fatal("connect over a dead dial must fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connect did not return within the dial bound — connAccess starves forever (pre-fix behaviour)")
	}

	// The mutex everything queues behind must be free again.
	reacquired := make(chan struct{})
	go func() {
		bind.connAccess.Lock()
		bind.connAccess.Unlock()
		close(reacquired)
	}()
	select {
	case <-reacquired:
	case <-time.After(time.Second):
		t.Fatal("connAccess still held after the failed dial")
	}
}

// After a successful dial the timeout context must stay alive until the
// connection generation dies (the SPEC 050 guard stays armed on it until the
// stream is up), and be released once the wireConn closes.
func TestConnectDialContextReleasedOnConnClose(t *testing.T) {
	dialer := &captureDialer{}
	bind := NewClientBind(context.Background(), logger.NOP(), dialer, true, netip.MustParseAddrPort("127.0.0.1:443"), [3]uint8{})
	_, _, err := bind.Open(0)
	if err != nil {
		t.Fatal(err)
	}

	serverConn, err := bind.connect()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-dialer.dialCtx.Done():
		t.Fatal("dial context cancelled on connect() return — would kill a healthy connection mid-raise (050 guard window)")
	default:
	}

	serverConn.Close()
	select {
	case <-dialer.dialCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("dial context not released after the connection generation died")
	}
}
