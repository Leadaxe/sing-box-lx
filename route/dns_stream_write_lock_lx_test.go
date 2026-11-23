package route

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"

	mDNS "github.com/miekg/dns"
)

// SPEC 157. hijackDNSStream must write responses through NewSerialWriteConn:
// a proxied conn is not safe for concurrent writes. The conn below reports
// any Write that starts while another one is in flight.

type lxWriteLockRouter struct {
	adapter.DNSRouter
}

func (r *lxWriteLockRouter) ExchangeAsync(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions, callback func(response *mDNS.Msg, err error)) {
	go func() {
		response := new(mDNS.Msg)
		response.SetReply(message)
		callback(response, nil)
	}()
}

type lxOverlapConn struct {
	net.Conn
	active     atomic.Int32
	overlapped atomic.Bool
}

func (c *lxOverlapConn) Write(b []byte) (int, error) {
	if c.active.Add(1) > 1 {
		c.overlapped.Store(true)
	}
	defer c.active.Add(-1)
	return c.Conn.Write(b)
}

func TestLxHijackDNSStreamSerializesResponseWrites(t *testing.T) {
	const queries = 16
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	overlapConn := &lxOverlapConn{Conn: serverConn}
	router := &Router{dns: &lxWriteLockRouter{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = router.hijackDNSStream(context.Background(), overlapConn, adapter.InboundContext{})
	}()
	clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	for i := range queries {
		message := new(mDNS.Msg)
		message.SetQuestion("example.com.", mDNS.TypeA)
		message.Id = uint16(i + 1)
		packed, err := message.Pack()
		if err != nil {
			t.Fatal(err)
		}
		request := binary.BigEndian.AppendUint16(nil, uint16(len(packed)))
		_, err = clientConn.Write(append(request, packed...))
		if err != nil {
			t.Fatal(err)
		}
	}
	// net.Pipe has no buffer: every response writer is parked in Write until
	// the client starts reading, so unserialized writers always overlap here.
	time.Sleep(50 * time.Millisecond)
	for range queries {
		var length uint16
		err := binary.Read(clientConn, binary.BigEndian, &length)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.CopyN(io.Discard, clientConn, int64(length))
		if err != nil {
			t.Fatal(err)
		}
	}
	clientConn.Close()
	<-done
	if overlapConn.overlapped.Load() {
		t.Fatal("stream DNS responses were written concurrently: hijackDNSStream lost NewSerialWriteConn")
	}
}
