package lxdatagram

// lx: the path under a datagram conn — a kernel socket or another outbound.
//
// Two things in this fork depend on what carries a datagram conn. Reads of a
// conn tunnelled through a proxy need kernel truncation semantics, a kernel
// socket already has them (SPEC 150, truncate.go). The sniff decoy goes out by
// path, lx.quic_sniff_decoy mode direct/detour (SPEC 151, decoy.go), for the
// QUIC clients and the AmneziaWG masquerade burst alike. Both ask the same question of the conn as
// dialled, before any wrapper of their own: is there a kernel descriptor
// under it. "Direct" is therefore a property of the dialled conn, not of the
// config: no detour, a detour to a direct outbound, or a detour to a group
// (selector, urltest, nested) whose current member is direct all classify as
// the direct path, and the answer follows the group on the next dial.

import (
	"net"
	"syscall"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// IsKernelSocket reports whether conn, as dialled and before any wrapper, is
// a kernel socket (the direct path) rather than a conn carried by another
// outbound.
func IsKernelSocket(conn net.Conn) bool {
	return kernelUnder(conn)
}

// IsKernelPacketConn is IsKernelSocket for an unconnected packet conn
// (ListenPacket).
func IsKernelPacketConn(conn net.PacketConn) bool {
	return kernelUnder(conn)
}

// kernelUnder reports whether a kernel descriptor lies under conn once the
// transparent wrappers are peeled (lx: SPEC 151/153): the replaceable readers
// UnwrapReader follows, sing's packet adapters a group in `detour` puts
// between its interrupt wrapper and the member's socket, and the power-report
// counters of the default dialer. The adapters and counters are not
// replaceable, so UnwrapReader stops at them and a socket dialled by a direct
// outbound behind a selector looked like a detour conn. A protocol conn of a
// proxy outbound is none of these and ends the walk.
func kernelUnder(current any) bool {
	for current != nil {
		if _, isSyscallConn := current.(syscall.Conn); isSyscallConn {
			return true
		}
		if counter, isCounter := current.(N.ReadCounter); isCounter {
			current, _ = counter.UnwrapReader()
			continue
		}
		if counter, isCounter := current.(N.PacketReadCounter); isCounter {
			current, _ = counter.UnwrapPacketReader()
			continue
		}
		if fallback, isFallback := current.(*bufio.FallbackPacketConn); isFallback {
			current = fallback.PacketConn
			continue
		}
		// A group in `detour` (selector, urltest) re-wraps the packet socket
		// of its member in sing's plain adapter, which is not replaceable.
		if extended, isExtended := current.(*bufio.ExtendedPacketConn); isExtended {
			current = extended.PacketConn
			continue
		}
		if u, ok := current.(N.ReaderWithUpstream); !ok || !u.ReaderReplaceable() {
			return false
		}
		if u, ok := current.(N.WithUpstreamReader); ok {
			current = u.UpstreamReader()
		} else if u, ok := current.(common.WithUpstream); ok {
			current = u.Upstream()
		} else {
			return false
		}
	}
	return false
}
