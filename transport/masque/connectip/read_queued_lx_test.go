package connectip

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
)

// queueStream is an Http3Stream whose ReceiveDatagram hands out queued
// datagrams first and otherwise waits for ctx, as quic-go's does.
type queueStream struct {
	mu        sync.Mutex
	queue     [][]byte
	closeOnce sync.Once
	closed    chan struct{}
}

func newQueueStream(datagrams ...[]byte) *queueStream {
	return &queueStream{queue: datagrams, closed: make(chan struct{})}
}

func (s *queueStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, context.Canceled
}

func (s *queueStream) Write(b []byte) (int, error) { return len(b), nil }

func (s *queueStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *queueStream) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if len(s.queue) > 0 {
		data := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		return data, nil
	}
	s.mu.Unlock()
	select {
	case <-s.closed:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *queueStream) SendDatagram([]byte) error { return nil }

func (s *queueStream) CancelRead(quic.StreamErrorCode) {}

// The pump drains a burst with ReadQueuedPacket: queued packets in order,
// datagrams the tunnel drops skipped, then nil without blocking.
func TestReadQueuedPacket(t *testing.T) {
	t.Parallel()
	packets := make([][]byte, 5)
	for i := range packets {
		packets[i] = ipv4Packet(40 + i)
		packets[i][4] = byte(i) // identification tells them apart
	}
	foreign := ipv4Packet(40)
	copy(foreign[16:20], []byte{10, 0, 0, 9}) // not our address
	datagrams := [][]byte{
		append([]byte{0}, packets[0]...),
		append([]byte{0}, packets[1]...),
		append([]byte{1}, packets[2]...), // context ID 1: not an IP payload
		append([]byte{0}, packets[2]...),
		append([]byte{0}, foreign...),
		append([]byte{0}, packets[3]...),
		append([]byte{0}, packets[4]...),
	}
	conn := NewProxiedConn(newQueueStream(datagrams...))
	defer conn.Close()
	conn.mu.Lock()
	conn.assignedAddresses = []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}
	conn.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i, want := range packets {
			got := conn.ReadQueuedPacket()
			if string(got) != string(want) {
				t.Errorf("packet %d: got % x, want % x", i, got[:min(len(got), 8)], want[:8])
				return
			}
		}
		for range 2 {
			if got := conn.ReadQueuedPacket(); got != nil {
				t.Errorf("empty queue: got % x, want nil", got)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadQueuedPacket blocked")
	}
}
