package lxdatagram

import (
	"context"
	"net"

	"github.com/sagernet/sing/common/bufio/deadline"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// ReadDeadlineDialer gives the UDP conns of a dialer a working read deadline
// where they lack one. lx: SPEC 146.
//
// quic-go does not close a PacketConn it was handed: a single-use transport
// that gives up sets a read deadline on the conn and waits for its read loop
// to return. Through a detour the "socket" may be a stream-tunnelled conn
// (vless XUDP over WebSocket, …) whose SetReadDeadline does nothing — the loop
// never wakes, the failed dial never returns, and whatever lock the caller
// holds across the dial stays held; a Close queued behind it hangs with it. A
// conn that reports NeedAdditionalReadDeadline gets a real deadline here; a
// kernel socket and a conn with deadlines of its own pass through untouched.
//
// Wrap outside Dialer: the truncation of SPEC 150 belongs closest to the
// detour conn, below the deadline.
func ReadDeadlineDialer(dialer N.Dialer) N.Dialer {
	return &readDeadlineDialer{Dialer: dialer}
}

type readDeadlineDialer struct {
	N.Dialer
}

func (d *readDeadlineDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if err != nil || N.NetworkName(network) != N.NetworkUDP {
		return conn, err
	}
	if deadline.NeedAdditionalReadDeadline(conn) {
		return deadline.NewConn(conn), nil
	}
	return conn, nil
}

func (d *readDeadlineDialer) Upstream() any {
	return d.Dialer
}
