package masque

// lx: SPECS/TASKS/108 — a remembered h2 leg is a shortcut, not a verdict. When
// it stops working `auto` must give h3 its turn instead of failing every dial
// until the process restarts.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/transport/masque"
)

func rememberH2(o *Outbound) {
	o.rememberNetwork("h2")
}

func TestAutoRememberedH2FailureTriesH3(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.legsForTest = scripted(&calls, nil, false, errors.New("tcp refused"))
	rememberH2(o)
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h3" {
		t.Fatalf("h3 must rescue a failed remembered h2: network=%q err=%v", network, err)
	}
	if strings.Join(calls, ",") != "h2,h3" {
		t.Fatalf("expected h2 then h3, each once, got %v", calls)
	}
	if got := o.effectiveNetwork(); got != "h3" {
		t.Fatalf("h3 must be remembered, got %q", got)
	}
}

func TestAutoRememberedH2FailureForgetsOnTotalFailure(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	o.legsForTest = scripted(&calls, errors.New("quic refused"), false, errors.New("tcp refused"))
	rememberH2(o)
	_, _, _, err := o.connect(context.Background(), o.effectiveNetwork())
	if err == nil {
		t.Fatal("both legs failed, connect must fail")
	}
	if !strings.Contains(err.Error(), "quic refused") || !strings.Contains(err.Error(), "tcp refused") {
		t.Fatalf("error must report both legs, got %v", err)
	}
	if strings.Join(calls, ",") != "h2,h3" {
		t.Fatalf("h2 must not be retried within one attempt, got %v", calls)
	}
	if o.autoNetwork.Load() != nil {
		t.Fatal("a failed remembered leg must be forgotten")
	}
}

// With h2 already gone there is nothing to fall back to: a slow h3 is waited
// for past the wall-clock budget instead of being abandoned.
func TestAutoRememberedH2FailureWaitsForSlowH3(t *testing.T) {
	o := newAutoOutbound(true, "h3")
	o.autoH3Delay = 20 * time.Millisecond
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			select {
			case <-time.After(120 * time.Millisecond):
				return stubCloser{}, nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			return nil, nil, errors.New("tcp refused")
		},
	}
	rememberH2(o)
	_, _, network, err := o.connect(context.Background(), o.effectiveNetwork())
	if err != nil || network != "h3" {
		t.Fatalf("slow h3 must not be cut by the fallback timer: network=%q err=%v", network, err)
	}
}

// A cancelled dial is not a verdict on h2: no h3 attempt, memory kept.
func TestAutoRememberedH2KeptOnCallerCancellation(t *testing.T) {
	var calls []string
	o := newAutoOutbound(true, "h3")
	ctx, cancel := context.WithCancel(context.Background())
	o.legsForTest = &connectLegs{
		h3: func(ctx context.Context, budget time.Duration) (io.Closer, masque.IpConn, error) {
			calls = append(calls, "h3")
			return stubCloser{}, nil, nil
		},
		h2: func(ctx context.Context) (io.Closer, masque.IpConn, error) {
			calls = append(calls, "h2")
			cancel()
			return nil, nil, ctx.Err()
		},
	}
	rememberH2(o)
	if _, _, _, err := o.connect(ctx, o.effectiveNetwork()); err == nil {
		t.Fatal("cancelled dial must fail")
	}
	if strings.Join(calls, ",") != "h2" {
		t.Fatalf("no h3 attempt after caller cancellation, got %v", calls)
	}
	if got := o.effectiveNetwork(); got != "h2" {
		t.Fatalf("memory must survive a cancelled dial, got %q", got)
	}
}

// Fixed h2 is the user's choice: it never turns into h3.
func TestFixedH2NeverTriesH3(t *testing.T) {
	var calls []string
	o := newAutoOutbound(false, "h2")
	o.legsForTest = scripted(&calls, nil, false, errors.New("tcp refused"))
	if _, _, _, err := o.connect(context.Background(), o.effectiveNetwork()); err == nil {
		t.Fatal("fixed h2 must fail when h2 fails")
	}
	if strings.Join(calls, ",") != "h2" {
		t.Fatalf("fixed h2 must not try h3, got %v", calls)
	}
}
