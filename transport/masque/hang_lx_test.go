package masque

// lx: SPECS/TASKS/108 — two waits that had no bound: Close behind a stuck h2
// writer, and ReadResponse on h3.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/quic-go"

	xhttp2 "golang.org/x/net/http2"
)

// net.Pipe is unbuffered: a write with nobody reading blocks, which is what a
// path that stopped draining looks like to the writer.
func TestH2CloseDoesNotWaitForStuckWriter(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	conn := &h2RawConn{
		tlsConn: local,
		framer:  xhttp2.NewFramer(local, local),
		recvCh:  make(chan []byte, 8),
		errCh:   make(chan error, 1),
		closed:  make(chan struct{}),
	}
	writeStarted := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		close(writeStarted)
		writeDone <- conn.writeData(make([]byte, 1200))
	}()
	<-writeStarted
	time.Sleep(50 * time.Millisecond) // let the writer take writeMu and park

	closeDone := make(chan struct{})
	started := time.Now()
	go func() {
		_ = conn.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close is parked behind the stuck writer")
	}
	if elapsed := time.Since(started); elapsed > 2*h2CloseGrace {
		t.Fatalf("Close took %v, want under %v", elapsed, 2*h2CloseGrace)
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("stuck write must fail once the conn is closed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stuck writer was not released by Close")
	}
}

// With the writer lock free the farewell RST_STREAM still goes out.
func TestH2CloseSendsResetWhenIdle(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	conn := &h2RawConn{
		tlsConn: local,
		framer:  xhttp2.NewFramer(local, local),
		recvCh:  make(chan []byte, 8),
		errCh:   make(chan error, 1),
		closed:  make(chan struct{}),
	}
	frames := make(chan xhttp2.Frame, 1)
	go func() {
		frame, err := xhttp2.NewFramer(remote, remote).ReadFrame()
		if err == nil {
			frames <- frame
		}
		close(frames)
	}()
	_ = conn.Close()
	select {
	case frame := <-frames:
		reset, isReset := frame.(*xhttp2.RSTStreamFrame)
		if !isReset || reset.StreamID != h2StreamID {
			t.Fatalf("want RST_STREAM on stream %d, got %v", h2StreamID, frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no frame reached the peer")
	}
}

// blockingStream answers ReadResponse only when cancelled, like an endpoint that
// took the connection and never replied to the CONNECT.
type blockingStream struct {
	cancelled chan struct{}
	once      sync.Once
	response  *http.Response
}

func (s *blockingStream) ReadResponse() (*http.Response, error) {
	if s.response != nil {
		return s.response, nil
	}
	<-s.cancelled
	return nil, errors.New("stream cancelled")
}

func (s *blockingStream) CancelRead(quic.StreamErrorCode) {
	s.once.Do(func() { close(s.cancelled) })
}

func (s *blockingStream) CancelWrite(quic.StreamErrorCode) {}

func TestReadResponseHonoursContext(t *testing.T) {
	stream := &blockingStream{cancelled: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := readResponse(ctx, stream)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want the context's error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readResponse ignored the context")
	}
}

// Once the response is in, a later cancel of the dial context must not touch
// the stream: the tunnel outlives the dial.
func TestReadResponseReleasesStreamOnSuccess(t *testing.T) {
	stream := &blockingStream{
		cancelled: make(chan struct{}),
		response:  &http.Response{StatusCode: http.StatusOK},
	}
	ctx, cancel := context.WithCancel(context.Background())
	response, err := readResponse(ctx, stream)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response=%v err=%v", response, err)
	}
	cancel()
	select {
	case <-stream.cancelled:
		t.Fatal("dial context cancelled a live stream")
	case <-time.After(100 * time.Millisecond):
	}
}
