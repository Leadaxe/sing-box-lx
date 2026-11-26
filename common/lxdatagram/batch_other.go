//go:build !linux

package lxdatagram

import "net"

func batchReads(conn net.Conn) net.Conn {
	return conn
}
