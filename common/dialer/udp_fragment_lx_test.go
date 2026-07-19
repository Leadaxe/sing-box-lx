// lx: SPEC 028 — regression tests for the udp_fragment / UDPFragmentDefault
// plumbing. The WireGuard endpoint (and MASQUE outbound) rely on
// UDPFragmentDefault=true reaching the real UDP socket as "DF clear": with DF
// set, an outer datagram larger than the path MTU is silently dropped instead
// of fragmented, which blackholes nested tunnels (AWG-over-AWG, MASQUE-over-AWG)
// and AWG s4 transport junk. These tests assert the socket flag itself, on both
// paths a WireGuard bind can take: the dialer (ClientBind) and the listener
// control (StdNetBind via UDPListenerControl).
package dialer

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func dialUDPForDF(t *testing.T, options option.DialerOptions) syscall.Conn {
	t.Helper()
	d, err := NewDefault(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialContext(context.Background(), N.NetworkUDP, M.ParseSocksaddr("127.0.0.1:9"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sysConn, isSysConn := conn.(syscall.Conn)
	if !isSysConn {
		t.Fatalf("dialed UDP conn %T does not expose SyscallConn", conn)
	}
	return sysConn
}

func listenUDPForDF(t *testing.T, options option.DialerOptions) syscall.Conn {
	t.Helper()
	d, err := NewDefault(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	listenerControl, _ := d.UDPListenerControl()
	listenConfig := net.ListenConfig{Control: listenerControl}
	packetConn, err := listenConfig.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetConn.Close() })
	sysConn, isSysConn := packetConn.(syscall.Conn)
	if !isSysConn {
		t.Fatalf("listened UDP conn %T does not expose SyscallConn", packetConn)
	}
	return sysConn
}

// Upstream default: no UDPFragmentDefault, no udp_fragment → DF is set on both
// the dial and listener paths. Pins the baseline the endpoint fix opts out of.
func TestUDPFragmentDFByDefault_LX(t *testing.T) {
	if !udpSocketDFSet(t, dialUDPForDF(t, option.DialerOptions{})) {
		t.Fatal("default dialer must set DF on dialed UDP sockets")
	}
	if !udpSocketDFSet(t, listenUDPForDF(t, option.DialerOptions{})) {
		t.Fatal("default dialer must set DF on listener-control UDP sockets")
	}
}

// UDPFragmentDefault=true (what the WireGuard endpoint and MASQUE outbound now
// set) → DF clear on both paths, so oversize outer datagrams fragment instead
// of vanishing.
func TestUDPFragmentDefaultClearsDF_LX(t *testing.T) {
	options := option.DialerOptions{UDPFragmentDefault: true}
	if udpSocketDFSet(t, dialUDPForDF(t, options)) {
		t.Fatal("UDPFragmentDefault=true must leave DF clear on dialed UDP sockets")
	}
	if udpSocketDFSet(t, listenUDPForDF(t, options)) {
		t.Fatal("UDPFragmentDefault=true must leave DF clear on listener-control UDP sockets")
	}
}

// Explicit user config always wins over the protocol default, in both
// directions.
func TestUDPFragmentExplicitOverride_LX(t *testing.T) {
	fragmentOff := false
	options := option.DialerOptions{UDPFragment: &fragmentOff, UDPFragmentDefault: true}
	if !udpSocketDFSet(t, dialUDPForDF(t, options)) {
		t.Fatal("udp_fragment=false must set DF even when the protocol default allows fragmentation")
	}
	fragmentOn := true
	options = option.DialerOptions{UDPFragment: &fragmentOn}
	if udpSocketDFSet(t, dialUDPForDF(t, options)) {
		t.Fatal("udp_fragment=true must leave DF clear even without a protocol default")
	}
}
