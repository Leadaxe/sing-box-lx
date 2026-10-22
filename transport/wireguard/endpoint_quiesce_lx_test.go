//go:build with_gvisor

// lx: SPEC 030 — the shutdown pre-pass. box.Close must close every WG/AWG UDP
// socket before the endpoint scopes run, so the per-device Close drain
// (stopping.Wait on the receive workers' ReadFrom) returns at once instead of
// serialising a socket close per endpoint. These tests drive a REAL endpoint
// (gVisor stack device, a listen_port socket on loopback, no peer traffic)
// and watch the socket itself: while the device is up the port is held, after
// the pre-pass it is free.
package wireguard

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service/pause"
)

// newQuiesceTestEndpoint starts a live endpoint bound to a fixed loopback
// listen_port under a default pause manager, and returns both.
func newQuiesceTestEndpoint(t *testing.T) (*Endpoint, pause.Manager, uint16) {
	t.Helper()
	ctx := pause.WithDefaultManager(context.Background())
	outboundDialer, err := dialer.NewDefault(ctx, option.DialerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	port := freeUDPPort(t)
	e, err := NewEndpoint(EndpointOptions{
		Context:    ctx,
		Logger:     logger.NOP(),
		Dialer:     outboundDialer,
		Name:       "wg-quiesce",
		MTU:        1408,
		ListenPort: port,
		Address:    []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
		PrivateKey: "iOx8sYFBnQjKMkTfTBaLZ5+DEHU4S3vzcSLp+HDaOWc=",
		Peers: []PeerOptions{{
			Endpoint:   mustSocksaddr("127.0.0.1:9"),
			PublicKey:  "eBpqZlJmSVBGNW9BOEZ4S3lRZTNQd0RhbWFnZTBTNTA=",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	})
	if err != nil {
		t.Fatalf("endpoint construction failed: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	for _, postStart := range []bool{false, true} {
		if err = e.Start(postStart); err != nil {
			t.Fatal(err)
		}
	}
	// device.Up runs off the tun device's EventUp, so the socket appears a beat
	// after Start returns.
	if !waitUDPPort(port, true, 2*time.Second) {
		t.Fatalf("precondition: a started endpoint must hold its listen_port %d", port)
	}
	return e, pause.ManagerFromContext(ctx), port
}

// udpPortHeld reports whether something still owns the wildcard UDP port —
// exactly the address StdNetBind listens on, so a bind attempt collides with
// the device's socket and nothing else.
func udpPortHeld(port uint16) bool {
	conn, err := net.ListenPacket("udp4", ":"+strconv.Itoa(int(port)))
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// waitUDPPort polls until the port is held (or free) or the window runs out.
func waitUDPPort(port uint16, held bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if udpPortHeld(port) == held {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return udpPortHeld(port) == held
}

// A device-pause broadcast must NOT touch the device: upstream 6364d9a5f ("Fix
// WireGuard endpoint stopping on device sleep") made WireGuard survive device
// sleep on purpose, and SPEC 020 keeps it that way. This is the constraint the
// pre-pass can no longer lean on.
func TestDevicePauseLeavesSocketOpen(t *testing.T) {
	_, manager, port := newQuiesceTestEndpoint(t)
	manager.DevicePause()
	defer manager.DeviceWake()
	time.Sleep(300 * time.Millisecond)
	if !udpPortHeld(port) {
		t.Fatalf("device pause closed the WireGuard socket on port %d — upstream keeps WG alive through device sleep", port)
	}
}

// The explicit pre-pass closes the socket: after QuiesceForShutdown the
// listen_port is released without the endpoint having been closed.
func TestQuiesceForShutdownClosesSocket(t *testing.T) {
	e, _, port := newQuiesceTestEndpoint(t)
	e.QuiesceForShutdown()
	if !waitUDPPort(port, false, 2*time.Second) {
		t.Fatalf("QuiesceForShutdown left the WireGuard socket on port %d open", port)
	}
	if e.device == nil {
		t.Fatal("QuiesceForShutdown must only bring the device down, not close it — Close owns the teardown")
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close after QuiesceForShutdown: %v", err)
	}
}

// Close after the pre-pass still runs the full teardown, and a pre-pass stamped
// before Close never applies after it (the SPEC 071 latest-wins contract).
func TestQuiesceForShutdownThenCloseIsClean(t *testing.T) {
	e, _, _ := newQuiesceTestEndpoint(t)
	e.pauseOpAccess.Lock()
	e.QuiesceForShutdown() // queued behind the lock
	e.pauseOpAccess.Unlock()
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if e.device != nil {
		t.Fatal("Close must release the device")
	}
	// A second quiesce on a closed endpoint is a no-op.
	e.QuiesceForShutdown()
	time.Sleep(100 * time.Millisecond)
}

// A never-started (nil device) endpoint and a torn-down one are safe to quiesce.
func TestQuiesceForShutdownNilDevice(t *testing.T) {
	e := &Endpoint{}
	e.QuiesceForShutdown()
	time.Sleep(50 * time.Millisecond)
	if e.pauseSeq.Load() == 0 {
		t.Fatal("QuiesceForShutdown must stamp pauseSeq so a stale pause event cannot undo it")
	}
}
