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
	cfg, err := resolve(Options{Seed: seed(1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := newShaper(cfg, realClock())
	defer sh.close()
	stuck, free := newStubPacket(true), newStubPacket(false)
	a, b := newPacketConn(sh, stuck, nil), newPacketConn(sh, free, nil)
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

// The event loop logs schedule entries at their moment without traffic.
func TestEventLoopLogsOnTime(t *testing.T) {
	cfg, err := resolve(Options{Seed: seed(1), Schedule: []ScheduleEntry{
		{At: "100ms", LinkParams: lp(LinkOptions{Outage: boolp(true)})},
		{At: "200ms", LinkParams: lp(LinkOptions{Outage: boolp(false)})},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := newShaper(cfg, realClock())
	defer sh.close()
	got := make(chan string, 4)
	start := time.Now()
	sh.logf = func(debug bool, text string) { got <- text }
	sh.startEvents()
	for _, want := range []string{"schedule at 100ms: up rate unlimited delay 0s outage; down rate unlimited delay 0s outage", "schedule at 200ms: up rate unlimited delay 0s; down rate unlimited delay 0s"} {
		select {
		case text := <-got:
			if text != want {
				t.Fatalf("log %q, want %q", text, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("no log %q", want)
		}
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 400*time.Millisecond {
		t.Errorf("second entry logged after %v", d)
	}
}
