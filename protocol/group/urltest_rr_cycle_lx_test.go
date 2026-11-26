// lx:begin SPEC 138 round_robin per-connection cost: the full life cycle

package group

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// cycleNode is a proxy-like node whose connection is a pipe to a destination that
// reads a request and answers it.
type cycleNode struct {
	adapter.Outbound
	tag string
}

func (n *cycleNode) Type() string           { return C.TypeVLESS }
func (n *cycleNode) Tag() string            { return n.tag }
func (n *cycleNode) Network() []string      { return []string{N.NetworkTCP} }
func (n *cycleNode) Dependencies() []string { return nil }

var cycleResponse = make([]byte, 512)

func (n *cycleNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		buffer := make([]byte, 256)
		if _, err := server.Read(buffer); err == nil {
			server.Write(cycleResponse)
		}
		server.Close()
	}()
	return client, nil
}

var cycleRequest = []byte("GET / HTTP/1.1\r\n\r\n")

// cycle runs one connection through the group as route/conn.go does: the dial,
// kickWriteHandshake, both copies, the client's request and the answer, the close.
// sniffed: the request was read by sniffing and is cached, so kickWriteHandshake
// writes it before the copies start.
func cycle(tb testing.TB, s *URLTest, ctx context.Context, sniffed bool) {
	user, inbound := net.Pipe()
	remote, err := s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.com", 443))
	if err != nil {
		tb.Fatal(err)
	}
	if N.NeedHandshakeForWrite(remote) {
		if sniffed {
			remote.Write(cycleRequest)
		} else {
			remote.Write(nil)
		}
	} else if sniffed {
		remote.Write(cycleRequest) // the copy writes the cached payload first
	}
	done := make(chan struct{}, 2)
	go func() {
		bufio.Copy(remote, inbound)
		remote.Close()
		done <- struct{}{}
	}()
	go func() {
		bufio.Copy(inbound, remote)
		inbound.Close()
		done <- struct{}{}
	}()
	if !sniffed {
		user.Write(cycleRequest)
	}
	io.Copy(io.Discard, user)
	user.Close()
	<-done
	<-done
}

func newCycleFixture(tb testing.TB) *URLTest {
	f := newHealFixture(tb, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	manager := f.g.outbound.(*fakeManager)
	for i, tag := range []string{"a", "b", "c"} {
		node := &cycleNode{tag: tag}
		manager.byTag[tag] = node
		f.g.outbounds[i] = node
	}
	return f.s
}

// The full life cycle of a short connection through the group: watched (the
// self-heal) and unwatched (a delay measurement: the group picks, nothing else).
func BenchmarkRRConnectionCycle(b *testing.B) {
	for _, sniffed := range []bool{false, true} {
		for _, watched := range []bool{false, true} {
			name := "unwatched"
			if watched {
				name = "watched"
			}
			if sniffed {
				name += "-sniffed"
			}
			b.Run(name, func(b *testing.B) {
				s := newCycleFixture(b)
				ctx := context.Background()
				if !watched {
					ctx = urltest.WithMeasureLx(ctx)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					cycle(b, s, ctx, sniffed)
				}
			})
		}
	}
}

// The wrapper is one allocation: the watch lives in it, and it reads and writes the
// node's connection without the adapters of bufio.NewExtendedReader/Writer.
func TestRRCycle_wrapperAllocatesOnce(t *testing.T) {
	s := &URLTest{group: &URLTestGroup{ctx: context.Background()}, lxURLTest: lxURLTest{balancer: &balancer{}}}
	watch := rrConnWatch{s: s, member: "a"}
	if allocs := testing.AllocsPerRun(1000, func() {
		newRRFirstByteConn(nopConn{}, &watch)
	}); allocs != 1 {
		t.Fatalf("the wrapper allocates %v objects, want 1", allocs)
	}
}

// lx:end SPEC 138
