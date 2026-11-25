package impair

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// stubPacket is a carrier: WriteTo blocks while block is set, else counts.
type stubPacket struct {
	block   chan struct{}
	written atomic.Int64
	closed  chan struct{}
}

func newStubPacket(blocking bool) *stubPacket {
	s := &stubPacket{closed: make(chan struct{})}
	if blocking {
		s.block = make(chan struct{})
	}
	return s
}

func (s *stubPacket) ReadFrom(p []byte) (int, net.Addr, error) {
	<-s.closed
	return 0, nil, net.ErrClosed
}

func (s *stubPacket) WriteTo(p []byte, addr net.Addr) (int, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-s.closed:
			return 0, net.ErrClosed
		}
	}
	s.written.Add(1)
	return len(p), nil
}

func (s *stubPacket) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
func (s *stubPacket) LocalAddr() net.Addr                { return nil }
func (s *stubPacket) SetDeadline(t time.Time) error      { return nil }
func (s *stubPacket) SetReadDeadline(t time.Time) error  { return nil }
func (s *stubPacket) SetWriteDeadline(t time.Time) error { return nil }

// A carrier that blocks on write holds back only its own connection: the
// up pump serves every datagram connection of the outbound.
func TestPacketBlockedCarrierIsolated(t *testing.T) {
	cfg, err := resolve(Options{Seed: seed(1)})
	if err != nil {
		t.Fatal(err)
	}
	sh := newShaper(cfg, realClock())
	defer sh.close()
	stuck, free := newStubPacket(true), newStubPacket(false)
	a, b := newPacketConn(sh, stuck), newPacketConn(sh, free)
	defer a.Close()
	defer b.Close()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}
	payload := make([]byte, 100)
	for range 10 {
		a.WriteTo(payload, addr)
	}
	for range 10 {
		b.WriteTo(payload, addr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for free.written.Load() < 10 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := free.written.Load(); got != 10 {
		t.Fatalf("free carrier got %d datagrams, want 10", got)
	}
	if got := stuck.written.Load(); got != 0 {
		t.Fatalf("blocked carrier wrote %d", got)
	}
}
