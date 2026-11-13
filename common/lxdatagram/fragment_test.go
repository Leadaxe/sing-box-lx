package lxdatagram

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 028 — the UDP conn handed to the QUIC library must not lead to the
// socket: neither directly nor by unwrapping.
func TestOwnedSocketDialer_hidesTheSocket(t *testing.T) {
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	var raw net.Conn = socket
	if _, isSyscallConn := raw.(syscall.Conn); !isSyscallConn {
		t.Fatal("precondition: a UDP socket is a syscall.Conn")
	}
	dialer := OwnedSocketDialer(&fixedDialer{conn: raw})

	udp, err := dialer.DialContext(context.Background(), N.NetworkUDP, M.ParseSocksaddr("192.0.2.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	if _, isSyscallConn := udp.(syscall.Conn); isSyscallConn {
		t.Fatal("the QUIC library would set DF on this socket: SyscallConn is still reachable")
	}
	if _, isUDPConn := common.Cast[*net.UDPConn](udp); isUDPConn {
		t.Fatal("the socket is still reachable by unwrapping")
	}

	tcp, err := dialer.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("192.0.2.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	if tcp != raw {
		t.Fatal("a TCP conn is handed out as it is")
	}
}
