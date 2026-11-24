package lxdatagram

// lx: batched reads of a UDP socket its owner hides from quic-go (SPEC 021).
//
// quic-go reads a socket it owns with recvmmsg; a socket handed to it behind a
// wrapper is read one datagram per system call. An outbound that keeps the
// socket to itself — MASQUE does, the outer datagram of its tunnel must stay
// fragmentable and quic-go would set "don't fragment" — pays that call for
// every packet of the tunnel. BatchReads puts the batch back under the
// wrapper: one recvmmsg fills a ring of buffers and Read hands them out one by
// one, so quic-go sees the same conn.
//
// A batch read alone gathers three or four datagrams: the reader keeps up with
// the socket. While traffic flows the reader therefore waits batchGather
// before the next call and takes what has arrived, some tens of datagrams at
// once. They then travel through quic-go, the tunnel pump and the userspace
// stack as a group, which is where the saving is (fewer wake-ups, and a group
// the stack can coalesce). The wait applies only after a read that returned
// more than one datagram, so a lone packet is never held.

import (
	"net"
	"time"
)

const (
	batchSize   = 32
	batchBuffer = 2048
	batchGather = 2 * time.Millisecond
)

// BatchReads returns conn with batched reads when a kernel UDP socket lies
// directly under it, and conn itself otherwise (a conn carried by another
// outbound, a platform without recvmmsg). The returned conn is read by one
// goroutine at a time.
func BatchReads(conn net.Conn) net.Conn {
	if !kernelUnder(conn) {
		return conn
	}
	return batchReads(conn)
}
