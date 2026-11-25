// lx:begin SPEC 138 round_robin first byte tests (stage 2)

package group

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
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
	if !provedAny(f.s.balancer) {
		t.Fatal("the first byte marks the network alive")
	}
	if N.NeedHandshakeForRead(conn) || !conn.ReaderReplaceable() {
		t.Fatal("after the first byte: no handshake pending, reads may bypass the wrapper")
	}
	conn.Close()
	if f.s.balancer.heal.pauseEnd() != nil || f.s.balancer.heal.roundBusy() {
		t.Fatal("a success starts no check")
	}
}

// Closed by the destination with no byte after the client sent: an implicit
// REFUSE_DST, a Q1 sign on the node. Closed with nothing sent, closed by us (CANCEL)
// or half-closed by the client first: nothing.
func TestRRFirstByte_silentClose(t *testing.T) {
	cases := []struct {
		name       string
		send       bool
		ourSide    bool
		halfClosed bool
		failure    bool
	}{
		{"sent, closed by the destination", true, false, false, true},
		{"nothing sent, closed by the destination", false, false, false, false},
		{"sent, closed by us", true, true, false, false},
		{"sent, half-closed by us, closed by the destination", true, false, true, false},
	}
	for _, c := range cases {
		client, server := net.Pipe()
		conn, f := watched(t, client)
		f.nodes["a"].siteDown.Store(true)
		conn.watch.track = f.s.balancer.trackOf("a")
		go func() {
			buffer := make([]byte, 8)
			if c.send {
				server.Read(buffer)
			}
			if !c.ourSide {
				time.Sleep(10 * time.Millisecond)
				server.Close()
			}
		}()
		if c.send {
			conn.Write([]byte("hi"))
		}
		if c.halfClosed {
			conn.LxClientFinished(true) // route/conn.go, past the wrapper once it is WriterReplaceable
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
		// An implicit REFUSE_DST waits as a Q1 sign for an ANSWER via a; it starts
		// no probe and no check (it is neither S1 nor S2). A close from our side is
		// CANCEL, a half-closed client gives no REFUSE_DST.
		if pending := conn.watch.track.pendingN.Load() == 1; pending != c.failure {
			t.Fatalf("%s: Q1 sign %v, want %v", c.name, pending, c.failure)
		}
		if f.probes("a") != 0 || f.s.balancer.heal.roundBusy() {
			t.Fatalf("%s: no probe, no check", c.name)
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
	if !provedAny(f.s.balancer) {
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

// A node with a raw TCP connection after its handshake (socks, http): the copy of
// sing tries copyDirect (splice on Linux) again after every re-selection of its
// path (CopyWithCounters loops back to it on ErrHandshakeCompleted). Before the
// first byte the wrapper keeps the copy off it; after the first byte the raw
// connection is reachable for both the direct read and the read waiter, through the
// group's interrupt wrapper as DialContext stacks them.
func TestRRFirstByte_directReadAfterFirstByte(t *testing.T) {
	client, server := tcpPair(t)
	conn, _ := watched(t, client)
	outer := interrupt.NewGroup().NewConn(conn, false)
	if N.SyscallAvailableForRead(outer) {
		t.Fatal("before the first byte the copy reads through the wrapper")
	}
	server.Write([]byte("x"))
	buffer := make([]byte, 8)
	if n, _ := outer.Read(buffer); n != 1 {
		t.Fatal("precondition: the first byte")
	}
	if !N.SyscallAvailableForRead(outer) {
		t.Fatal("after the first byte the direct read (splice) is available")
	}
	if _, raw := N.SyscallConnForRead(outer); raw == nil {
		t.Fatal("after the first byte the raw connection is reached through the wrappers")
	}
	if N.SyscallAvailableForWrite(outer) {
		t.Fatal("the upload path waits for the client's first write")
	}
	outer.Write([]byte("hi"))
	if !N.SyscallAvailableForWrite(outer) {
		t.Fatal("once the client has written the upload path is direct too")
	}
}

// earlyConn needs a write handshake, as a VLESS/VMess client connection does: it
// completes with the first write, an empty one included. It counts the writes.
type earlyConn struct {
	net.Conn
	need   atomic.Bool
	writes atomic.Int32
	empty  atomic.Int32
}

func (c *earlyConn) NeedHandshakeForWrite() bool { return c.need.Load() }

func (c *earlyConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	if len(p) == 0 {
		c.empty.Add(1)
	}
	c.need.Store(false)
	return c.Conn.Write(p)
}

// The wrapper declares a write handshake until the client's first write, and the
// node's own as long as that one lasts. The empty nudge of kickWriteHandshake
// (route/conn.go) before the first write reaches the node's connection only when
// that one needs it — as without the wrapper — and is not the client sending. After
// the client's first write the wrapper steps aside for writes.
func TestRRFirstByte_writeHandshake(t *testing.T) {
	for _, need := range []bool{true, false} {
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		lower := &earlyConn{Conn: client}
		lower.need.Store(need)
		conn, _ := watched(t, lower)
		outer := interrupt.NewGroup().NewConn(conn, false)
		if !N.NeedHandshakeForWrite(outer) || conn.WriterReplaceable() {
			t.Fatalf("need=%v: before the client's first write the wrapper holds the write path", need)
		}
		// kickWriteHandshake: an empty write.
		if _, err := outer.Write(nil); err != nil {
			t.Fatal(err)
		}
		if got := lower.empty.Load(); (got == 1) != need {
			t.Fatalf("need=%v: the empty nudge reaches the node's connection only when it needs it, got %d", need, got)
		}
		if conn.state.Load()&rrStateWrote != 0 {
			t.Fatalf("need=%v: the nudge is not the client sending", need)
		}
		if !N.NeedHandshakeForWrite(outer) || conn.WriterReplaceable() {
			t.Fatalf("need=%v: after the nudge the wrapper still waits for the client", need)
		}
		if _, err := outer.Write([]byte("hi")); err != nil {
			t.Fatal(err)
		}
		if conn.state.Load()&rrStateWrote == 0 || !conn.WriterReplaceable() || N.NeedHandshakeForWrite(outer) {
			t.Fatalf("need=%v: after the client's first write the wrapper steps aside for writes", need)
		}
		if N.UnwrapWriter(outer) != net.Conn(lower) {
			t.Fatalf("need=%v: the write path unwraps to the node's connection", need)
		}
		client.Close()
		server.Close()
	}
	// The node's own write handshake keeps the copy on the wrapper until it completes.
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()
	lower := &earlyConn{Conn: client}
	conn, _ := watched(t, lower)
	conn.state.Store(rrStateWrote)
	lower.need.Store(true)
	if !conn.NeedHandshakeForWrite() {
		t.Fatal("the node's write handshake is reported through the wrapper")
	}
}

// writeStackConn counts the writes of the copy into the node's connection, and how
// many of them came through the wrapper.
type writeStackConn struct {
	net.Conn
	writes  atomic.Int32
	wrapped atomic.Int32
}

func (c *writeStackConn) note() {
	c.writes.Add(1)
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "rrFirstByteConn") {
			c.wrapped.Add(1)
			return
		}
		if !more {
			return
		}
	}
}

func (c *writeStackConn) Write(p []byte) (int, error) {
	c.note()
	return c.Conn.Write(p)
}

func (c *writeStackConn) Upstream() any { return c.Conn }

// The upload copy of sing writes through the wrapper until the client's first write
// completes the "handshake"; then it re-selects its path and writes into the
// node's connection directly. The data arrive intact.
func TestRRFirstByte_copyReselectsWritePath(t *testing.T) {
	clientIn, clientFar := tcpPair(t)
	nodeConn, nodeFar := tcpPair(t)
	lower := &writeStackConn{Conn: nodeConn}
	conn, _ := watched(t, lower)
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024)
	go func() {
		for sent := 0; sent < len(payload); sent += 4096 {
			if _, err := clientFar.Write(payload[sent : sent+4096]); err != nil {
				return
			}
		}
		clientFar.(*net.TCPConn).CloseWrite()
	}()
	received := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(nodeFar)
		received <- data
	}()
	if _, err := bufio.Copy(interrupt.NewGroup().NewConn(conn, false), clientIn); err != nil {
		t.Fatal(err)
	}
	nodeConn.(*net.TCPConn).CloseWrite()
	if data := <-received; !bytes.Equal(data, payload) {
		t.Fatalf("the data must arrive intact: %d of %d bytes", len(data), len(payload))
	}
	if lower.writes.Load() < 2 {
		t.Fatalf("precondition: several writes, got %d", lower.writes.Load())
	}
	if lower.wrapped.Load() != 1 {
		t.Fatalf("only the client's first write goes through the wrapper, got %d of %d", lower.wrapped.Load(), lower.writes.Load())
	}
}

// A direct node is not wrapped (splice stays); its open is the first byte. A
// proxy node is wrapped under the group's interrupt wrapper, and interrupting the
// group still closes the connection — a close by us, not a failure.
func TestRRFirstByte_directNotWrappedInterruptWorks(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	for _, node := range f.nodes {
		node.direct = true
	}
	f.s.balancer.stickyHash = nil
	conn, err := f.s.DialContext(context.Background(), N.NetworkTCP, siteAddr("example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := common.Cast[*rrFirstByteConn](conn); wrapped {
		t.Fatal("a direct node must not be wrapped")
	}
	conn.Close()
	if !provedAny(f.s.balancer) {
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
	if s.s.balancer.heal.roundBusy() || s.s.balancer.heal.pauseEnd() != nil {
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
				s := &URLTest{group: &URLTestGroup{ctx: context.Background()}, lxURLTest: lxURLTest{balancer: &balancer{}}}
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

// The cost of the wrapper on the upload: the client's stream (a loopback TCP
// connection) copied into the node's connection (another loopback TCP connection)
// as is and through the wrapper, under the group's interrupt wrapper as
// DialContext stacks them. The node's connection supports the fast write paths of
// sing; the wrapper must not take them away after the client's first write.
func BenchmarkRRFirstByteUpload(b *testing.B) {
	const chunk = 32 * 1024
	for _, wrap := range []bool{false, true} {
		name := "plain"
		if wrap {
			name = "wrapped"
		}
		b.Run(name, func(b *testing.B) {
			clientIn, clientFar := tcpPair(b)
			nodeConn, nodeFar := tcpPair(b)
			var conn net.Conn = nodeConn
			if wrap {
				s := &URLTest{group: &URLTestGroup{ctx: context.Background()}, lxURLTest: lxURLTest{balancer: &balancer{}}}
				conn = newRRFirstByteConn(nodeConn, &rrConnWatch{s: s, member: "a", start: time.Now()})
			}
			conn = interrupt.NewGroup().NewConn(conn, false)
			data := make([]byte, chunk)
			b.SetBytes(chunk)
			b.ReportAllocs()
			drained := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, nodeFar)
				close(drained)
			}()
			b.ResetTimer()
			go func() {
				for i := 0; i < b.N; i++ {
					if _, err := clientFar.Write(data); err != nil {
						return
					}
				}
				clientFar.(*net.TCPConn).CloseWrite()
			}()
			if _, err := bufio.Copy(conn, clientIn); err != nil {
				b.Fatal(err)
			}
			nodeConn.(*net.TCPConn).CloseWrite()
			<-drained
		})
	}
}

// The per-connection cost: wrap, write, first byte, close.
func BenchmarkRRFirstByteSetup(b *testing.B) {
	s := &URLTest{group: &URLTestGroup{ctx: context.Background()}, lxURLTest: lxURLTest{balancer: &balancer{}}}
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
