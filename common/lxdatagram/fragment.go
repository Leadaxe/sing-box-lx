package lxdatagram

import (
	"context"
	"net"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// OwnedSocketDialer keeps quic-go's hands off the UDP socket a dialer hands
// out, so the dialer's own `udp_fragment` setting stays in force. lx: SPEC 028.
//
// quic-go, given a conn it can reach the socket of, sets "don't fragment" on
// it (IP_PMTUDISC_PROBE on Linux) whatever the dialer configured. A tunnel's
// outer datagram is the inner packet plus the encapsulation: masque-client
// opens with `mtu` + 51 bytes of QUIC payload, 1359 bytes on the wire for the
// default mtu. On a path narrower than that — the node nested in another
// tunnel, a mobile uplink — every such datagram is dropped, the handshake
// never completes and HTTP/3 is lost; with fragmentation allowed the kernel
// splits it and the tunnel comes up (stand, eth0 mtu 1300: 1.14 up on h3, 1.15
// down to h2 until this wrapper). The conn is handed on as a plain net.Conn:
// quic-go then leaves the socket as its owner configured it. The price is
// quic-go's socket tuning (GSO, ECN, its receive buffer size), which the 1.14
// outbound never had either.
func OwnedSocketDialer(dialer N.Dialer) N.Dialer {
	return &ownedSocketDialer{Dialer: dialer}
}

type ownedSocketDialer struct {
	N.Dialer
}

func (d *ownedSocketDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err != nil || N.NetworkName(network) != N.NetworkUDP {
		return conn, err
	}
	return &ownedConn{Conn: conn}, nil
}

func (d *ownedSocketDialer) Upstream() any {
	return d.Dialer
}

// ownedConn exposes the net.Conn methods of the conn and nothing else: no
// SyscallConn, no upstream to unwrap to.
type ownedConn struct {
	net.Conn
}
