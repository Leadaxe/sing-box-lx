package lxdatagram

import (
	"net"
	"testing"

	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// The default dialer wraps its sockets in the power-report counters, which
// are not replaceable readers; a socket dialled by a direct outbound (the
// detour of a tunnel pointing at direct) must still classify as the direct
// path, and a counted proxy conn must not.
func TestKernelSocketUnderDialerCounters(t *testing.T) {
	count := []N.CountFunc{func(int64) {}}

	kernel, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	if !IsKernelSocket(bufio.NewCounterConn(&replaceableConn{Conn: kernel}, count, count)) {
		t.Fatal("a counted kernel socket must classify as kernel")
	}
	if IsKernelSocket(bufio.NewCounterConn(newFramedConn(), count, count)) {
		t.Fatal("a counted detour conn must not classify as kernel")
	}

	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !IsKernelPacketConn(bufio.NewNetPacketConn(bufio.NewCounterPacketConn(bufio.NewPacketConn(listener), count, count))) {
		t.Fatal("a counted kernel packet socket must classify as kernel")
	}

	// A chain of groups (selector in selector, urltest) ending in direct: each
	// group adds an interrupt wrapper, the socket under them is still kernel.
	outer, inner := interrupt.NewGroup(), interrupt.NewGroup()
	counted := bufio.NewCounterConn(&replaceableConn{Conn: kernel}, count, count)
	if !IsKernelSocket(outer.NewConn(inner.NewConn(counted, false), false)) {
		t.Fatal("a kernel socket under two groups must classify as kernel")
	}
	if IsKernelSocket(outer.NewConn(inner.NewConn(newFramedConn(), false), false)) {
		t.Fatal("a detour conn under two groups must not classify as kernel")
	}
	countedPacket := bufio.NewNetPacketConn(bufio.NewCounterPacketConn(bufio.NewPacketConn(listener), count, count))
	if !IsKernelPacketConn(outer.NewPacketConn(inner.NewPacketConn(countedPacket, false), false)) {
		t.Fatal("a kernel packet socket under two groups must classify as kernel")
	}
}

// trackedPacketConn mimics route.trackedPacketConn, the wrapper the default
// dialer puts on a ListenPacket socket.
type trackedPacketConn struct {
	net.PacketConn
}

func (c *trackedPacketConn) Upstream() any           { return bufio.NewPacketConn(c.PacketConn) }
func (c *trackedPacketConn) ReaderReplaceable() bool { return true }

// An AmneziaWG endpoint with a domain peer (or several peers) opens its socket
// with ListenPacket; with a selector in `detour` set to direct the socket
// comes back as interrupt.PacketConn over the tracked kernel socket.
func TestKernelPacketSocketUnderSelector(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	direct := &trackedPacketConn{PacketConn: listener}
	if !IsKernelPacketConn(direct) {
		t.Fatal("a tracked kernel packet socket must classify as kernel")
	}
	selector := interrupt.NewGroup()
	if !IsKernelPacketConn(selector.NewPacketConn(direct, false)) {
		t.Fatal("a kernel packet socket under a selector must classify as kernel")
	}
	if !IsKernelPacketConn(interrupt.NewGroup().NewPacketConn(selector.NewPacketConn(direct, false), false)) {
		t.Fatal("a kernel packet socket under two selectors must classify as kernel")
	}
}
