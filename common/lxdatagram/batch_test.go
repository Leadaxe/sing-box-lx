package lxdatagram

import "testing"

// A conn without a kernel socket under it keeps its own reads.
func TestBatchReadsKeepsNonKernelConn(t *testing.T) {
	t.Parallel()
	framed := newFramedConn(datagram(100, 0xa1))
	defer framed.Close()
	if conn := BatchReads(framed); conn != framed {
		t.Fatalf("BatchReads wrapped a non-kernel conn: %T", conn)
	}
}
