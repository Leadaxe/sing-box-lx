package lxhandshake

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func setTimeout(t *testing.T, timeout time.Duration) {
	original := Timeout
	Timeout = timeout
	t.Cleanup(func() { Timeout = original })
}

func TestErrTimeoutIsDeadlineExceeded(t *testing.T) {
	if !errors.Is(ErrTimeout, os.ErrDeadlineExceeded) {
		t.Fatal("ErrTimeout does not match os.ErrDeadlineExceeded")
	}
	var netErr net.Error
	if !errors.As(ErrTimeout, &netErr) || !netErr.Timeout() {
		t.Fatal("ErrTimeout is not a timeout net.Error")
	}
}

// TestContextTimerStartsOnlyAfterDial: without a dial through the wrapper the
// context never times out, and its cancel ends it as a cancellation.
func TestContextTimerStartsOnlyAfterDial(t *testing.T) {
	setTimeout(t, 50*time.Millisecond)
	ctx, cancel := Context(context.Background())
	if _, loaded := ctx.Deadline(); loaded {
		t.Fatal("the handshake context exposes a deadline")
	}
	time.Sleep(150 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("the context ended without a dial: %v", err)
	}
	cancel()
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled after cancel, got %v", err)
	}
}

func TestContextTimerReportsTimeout(t *testing.T) {
	setTimeout(t, 50*time.Millisecond)
	ctx, cancel := Context(context.Background())
	defer cancel()
	ctx.(*handshakeContext).startTimer()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { close(stopped) })
	defer stop()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the timer did not end the context")
	}
	if err := ctx.Err(); err != ErrTimeout {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}

// deadlineIgnoringConn accepts SetDeadline and does nothing, like a wrapper
// that forgets to forward it.
type deadlineIgnoringConn struct {
	net.Conn
}

func (deadlineIgnoringConn) SetDeadline(time.Time) error { return nil }

func TestGuard(t *testing.T) {
	for _, mode := range []string{"deadline", "ignored-deadline", "cancel", "success"} {
		t.Run(mode, func(t *testing.T) {
			setTimeout(t, 100*time.Millisecond)
			client, server := net.Pipe()
			defer server.Close()
			var conn net.Conn = client
			if mode == "ignored-deadline" {
				conn = deadlineIgnoringConn{client}
			}
			defer conn.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			finish := Guard(ctx, conn)
			if mode == "cancel" {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			if mode == "success" {
				go server.Write([]byte("x"))
			}
			start := time.Now()
			_, err := conn.Read(make([]byte, 1))
			elapsed := time.Since(start)
			finishErr := finish()
			if (finishErr == nil) != (mode == "success") && mode != "deadline" {
				t.Fatalf("finish reported %v", finishErr)
			}
			switch mode {
			case "deadline":
				if !errors.Is(err, os.ErrDeadlineExceeded) || elapsed > time.Second {
					t.Fatalf("got %v after %v", err, elapsed)
				}
			case "ignored-deadline":
				if err == nil || elapsed > Timeout+guardCloseGrace+time.Second {
					t.Fatalf("got %v after %v", err, elapsed)
				}
			case "cancel":
				if err == nil || elapsed > time.Second {
					t.Fatalf("got %v after %v", err, elapsed)
				}
			case "success":
				if err != nil {
					t.Fatal(err)
				}
				// Nothing armed by Guard may outlive finish.
				cancel()
				time.Sleep(3 * Timeout)
				go server.Write([]byte("y"))
				if _, err := conn.Read(make([]byte, 1)); err != nil {
					t.Fatal("the conn was cut after finish: ", err)
				}
			}
		})
	}
}

// The exchange succeeds and, at the same moment, ctx ends: the context watch has
// closed conn before finish could stop it. finish must report it, so the caller
// does not hand out a closed conn as a success.
func TestGuardCancelBeforeFinish(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	finish := Guard(ctx, client)
	cancel()
	// The watch runs in its own goroutine: wait until it has closed conn.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Write([]byte("x")); errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish after the context closed conn: got %v, want context.Canceled", err)
	}
	if err := finish(); !errors.Is(err, context.Canceled) {
		t.Fatal("finish is idempotent")
	}
}
