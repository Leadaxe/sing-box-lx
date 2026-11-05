package wireguard

// lx:begin SPEC 052 stack connect deadline
//
// The blackhole regression test lived on gVisor's channel NIC and left with
// it; the deadline now enters the sing-tun stack dial in device_stack.go
// (DialTCP honours its context), so the contract under test is the budget.

import (
	"context"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
)

// TestConnectContextLx_budget pins the deadline contract: a bare parent gets
// the probe-aligned C.TCPTimeout budget (SPEC 052 §S5: user deadline must not
// be below the probe budget), and a parent with an EARLIER deadline keeps it —
// the wrapper must never extend.
func TestConnectContextLx_budget(t *testing.T) {
	ctx, cancel := connectContextLx(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("connect ctx must carry a deadline")
	}
	if remaining := time.Until(deadline); remaining > C.TCPTimeout || remaining < C.TCPTimeout-time.Second {
		t.Fatalf("budget must be ~C.TCPTimeout (%v), got %v", C.TCPTimeout, remaining)
	}

	parent, parentCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer parentCancel()
	child, childCancel := connectContextLx(parent)
	defer childCancel()
	childDeadline, _ := child.Deadline()
	if time.Until(childDeadline) > 100*time.Millisecond {
		t.Fatal("an earlier parent deadline must win — the wrapper must never extend")
	}
}

// lx:end SPEC 052
