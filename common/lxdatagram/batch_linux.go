package lxdatagram

import (
	"net"
	"time"

	"github.com/sagernet/sing/common"

	"golang.org/x/net/ipv4"
)

type batchConn struct {
	net.Conn
	socket   *ipv4.PacketConn
	messages []ipv4.Message
	count    int
	next     int
	flowing  bool
}

func batchReads(conn net.Conn) net.Conn {
	udpConn, isUDP := common.Cast[*net.UDPConn](conn)
	if !isUDP {
		return conn
	}
	c := &batchConn{
		Conn: conn,
		// recvmmsg is the same call for both address families; x/net exposes it
		// through either packet conn.
		socket:   ipv4.NewPacketConn(udpConn),
		messages: make([]ipv4.Message, batchSize),
	}
	for i := range c.messages {
		c.messages[i].Buffers = [][]byte{make([]byte, batchBuffer)}
	}
	return c
}

func (c *batchConn) Read(p []byte) (int, error) {
	if c.next == c.count {
		if c.flowing {
			time.Sleep(batchGather)
		}
		count, err := c.socket.ReadBatch(c.messages, 0)
		if err != nil {
			c.flowing = false
			return 0, err
		}
		c.count, c.next = count, 0
		c.flowing = count > 1
	}
	message := &c.messages[c.next]
	c.next++
	// A datagram longer than p is cut, as the kernel cuts it for a plain read.
	return copy(p, message.Buffers[0][:message.N]), nil
}

func (c *batchConn) Upstream() any {
	return c.Conn
}
