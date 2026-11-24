// lx:begin SPEC 138 round_robin first byte tests (stage 2)

package group

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// tcpPair returns the two ends of one loopback TCP connection.
func tcpPair(t testing.TB) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

// watched wraps conn for member "a" of a fresh group and returns the wrapper.
func watched(t *testing.T, conn net.Conn) (*rrFirstByteConn, *healFixture) {
	t.Helper()
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	return newRRFirstByteConn(conn, &rrConnWatch{s: f.s, member: "a", start: time.Now()}), f
}

// The first byte from the destination is the success: the group's "network alive"
// mark moves, and the wrapper steps aside for reads.
func TestRRFirstByte_success(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn, f := watched(t, client)
	go func() {
		buffer := make([]byte, 8)
		server.Read(buffer)
		server.Write([]byte("ok"))
	}()
	if !N.NeedHandshakeForRead(conn) || conn.ReaderReplaceable() {
		t.Fatal("before the first byte: handshake pending, reads go through the wrapper")
	}
	conn.Write([]byte("hi"))
	buffer := make([]byte, 8)
	if n, _ := conn.Read(buffer); n == 0 {
		t.Fatal("precondition: an answer")
	}
	if f.s.balancer.heal.lastByte.Load() == 0 {
		t.Fatal("the first byte marks the network alive")
	}
	if N.NeedHandshakeForRead(conn) || !conn.ReaderReplaceable() {
		t.Fatal("after the first byte: no handshake pending, reads may bypass the wrapper")
	}
	conn.Close()
	if f.s.balancer.heal.lastCheckEnd.Load() != nil || f.s.balancer.heal.running.Load() {
		t.Fatal("a success starts no check")
	}
}

// Closed by the destination with no byte after the client sent: a failure — the
// check runs. Closed with nothing sent, or closed by us: no failure.
func TestRRFirstByte_silentClose(t *testing.T) {
	cases := []struct {
		name    string
		send    bool
		ourSide bool
		failure bool
	}{
		{"sent, closed by the destination", true, false, true},
		{"nothing sent, closed by the destination", false, false, false},
		{"sent, closed by us", true, true, false},
	}
	for _, c := range cases {
		client, server := net.Pipe()
		conn, f := watched(t, client)
		f.nodes["a"].siteDown.Store(true)
		go func() {
			buffer := make([]byte, 8)
			if c.send {
				server.Read(buffer)
			}
			if !c.ourSide {
				server.Close()
			}
		}()
		if c.send {
			conn.Write([]byte("hi"))
		}
		if c.ourSide {
			go func() {
				time.Sleep(20 * time.Millisecond)
				conn.Close()
			}()
		}
		buffer := make([]byte, 8)
		if n, _ := conn.Read(buffer); n != 0 {
			t.Fatalf("%s: precondition: no byte", c.name)
		}
		conn.Close()
		server.Close()
		time.Sleep(20 * time.Millisecond)
		started := f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastCheckEnd.Load() != nil
		if started != c.failure {
			t.Fatalf("%s: check started %v, want %v", c.name, started, c.failure)
		}
		if c.failure {
			f.waitCheck(t)
		}
	}
}

// countingConn counts how many times the copy asks it for a read waiter: a second
// request means the copy re-selected its path after the first byte.
type countingConn struct {
	net.Conn
	creates atomic.Int32
}

func (c *countingConn) CreateReadWaiter() (N.ReadWaiter, bool) {
	c.creates.Add(1)
	return bufio.CreateReadWaiter(c.Conn)
}

func (c *countingConn) Upstream() any { return c.Conn }

// The copy of sing reads through the read waiter of the node's connection with the
// wrapper peeking at the first buffer; the first byte completes the "handshake",
// and the copy re-selects its path and gets the bare read waiter. The data arrive
// intact.
func TestRRFirstByte_copyReselectsPath(t *testing.T) {
	client, server := tcpPair(t)
	lower := &countingConn{Conn: client}
	conn, f := watched(t, lower)
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024)
	go func() {
		server.Write(payload)
		server.Close()
	}()
	var received bytes.Buffer
	if _, err := bufio.Copy(&received, interrupt.NewGroup().NewConn(conn, false)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatalf("the data must arrive intact: %d of %d bytes", received.Len(), len(payload))
	}
	if lower.creates.Load() != 2 {
		t.Fatalf("the copy must ask for the read waiter twice (before and after the first byte), got %d", lower.creates.Load())
	}
	if f.s.balancer.heal.lastByte.Load() == 0 {
		t.Fatal("the first byte seen by the peeking waiter")
	}
	if waiter, ok := conn.CreateReadWaiter(); !ok {
		t.Fatal("a read waiter of the node's connection")
	} else if _, peeking := waiter.(*rrFirstByteReadWaiter); peeking {
		t.Fatal("after the first byte the waiter is the node's own")
	}
	handshake := N.NewHandshakeState(conn, io.Discard)
	if handshake.Upgradable() {
		t.Fatal("after the first byte no handshake is pending")
	}
}

// earlyConn needs a write handshake, as a VLESS/VMess client connection does.
type earlyConn struct {
	net.Conn
	need bool
}

func (c *earlyConn) NeedHandshakeForWrite() bool { return c.need }

// The wrapper does not change kickWriteHandshake (route/conn.go): the write
// handshake of the node's connection is found through it, as without it.
func TestRRFirstByte_writeHandshakeUnchanged(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	for _, need := range []bool{true, false} {
		lower := &earlyConn{Conn: client, need: need}
		conn, _ := watched(t, lower)
		outer := interrupt.NewGroup().NewConn(conn, false)
		if N.NeedHandshakeForWrite(outer) != need {
			t.Fatalf("NeedHandshakeForWrite through the wrapper must be the node's (%v)", need)
		}
		if _, declares := any(conn).(N.EarlyWriter); declares {
			t.Fatal("the wrapper must not declare a write handshake of its own")
		}
		if conn.WriterReplaceable() {
			t.Fatal("writes go through the wrapper")
		}
	}
}

// A direct node is not wrapped (splice stays); its open is the first byte. A
// proxy node is wrapped under the group's interrupt wrapper, and interrupting the
// group still closes the connection — a close by us, not a failure.
func TestRRFirstByte_directNotWrappedInterruptWorks(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	f.s.balancer.stickyHash = nil
	conn, err := f.s.DialContext(context.Background(), N.NetworkTCP, siteAddr("example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := common.Cast[*rrFirstByteConn](conn); wrapped {
		t.Fatal("a direct node must not be wrapped")
	}
	conn.Close()
	if f.s.balancer.heal.lastByte.Load() == 0 {
		t.Fatal("the open of a direct node counts as the first byte")
	}

	s := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	s.s.balancer.stickyHash = nil
	s.fb["a"].set(&s.fb["a"].mute, "example.org", true)
	conn, err = s.s.DialContext(context.Background(), N.NetworkTCP, siteAddr("example.org"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, wrapped := conn.(*interrupt.Conn); !wrapped {
		t.Fatal("the interrupt wrapper stays on top")
	}
	if _, wrapped := common.Cast[*rrFirstByteConn](conn); !wrapped {
		t.Fatal("a proxy node must be wrapped")
	}
	conn.Write([]byte("hi"))
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8)
		_, err := conn.Read(buffer)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	s.g.interruptGroup.Interrupt(true)
	select {
	case err := <-done:
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("interrupting the group must close the connection, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interrupting the group must close the connection")
	}
	time.Sleep(20 * time.Millisecond)
	if s.s.balancer.heal.running.Load() || s.s.balancer.heal.lastCheckEnd.Load() != nil {
		t.Fatal("an interrupt is a close by us, not a failure")
	}
}

// The cost of the wrapper on a copy: the same loopback stream read through the
// node's connection as is and through the wrapper (wrapper + interrupt wrapper vs
// the interrupt wrapper alone, as DialContext stacks them).
func BenchmarkRRFirstByteCopy(b *testing.B) {
	const chunk = 32 * 1024
	for _, wrap := range []bool{false, true} {
		name := "plain"
		if wrap {
			name = "wrapped"
		}
		b.Run(name, func(b *testing.B) {
			client, server := tcpPair(b)
			var conn net.Conn = client
			if wrap {
				s := &URLTest{balancer: &balancer{}, group: &URLTestGroup{ctx: context.Background()}}
				conn = newRRFirstByteConn(client, &rrConnWatch{s: s, member: "a", start: time.Now()})
			}
			conn = interrupt.NewGroup().NewConn(conn, false)
			data := make([]byte, chunk)
			b.SetBytes(chunk)
			b.ReportAllocs()
			b.ResetTimer()
			go func() {
				for i := 0; i < b.N; i++ {
					if _, err := server.Write(data); err != nil {
						return
					}
				}
				server.(*net.TCPConn).CloseWrite()
			}()
			if _, err := bufio.Copy(io.Discard, conn); err != nil {
				b.Fatal(err)
			}
		})
	}
}

// The per-connection cost: wrap, write, first byte, close.
func BenchmarkRRFirstByteSetup(b *testing.B) {
	s := &URLTest{balancer: &balancer{}, group: &URLTestGroup{ctx: context.Background()}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		conn := newRRFirstByteConn(nopConn{}, &rrConnWatch{s: s, member: "a"})
		conn.Write([]byte("x"))
		buffer := make([]byte, 1)
		conn.Read(buffer)
		conn.Close()
	}
}

type nopConn struct{ net.Conn }

func (nopConn) Read(p []byte) (int, error)  { return len(p), nil }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }

// lx:end SPEC 138
