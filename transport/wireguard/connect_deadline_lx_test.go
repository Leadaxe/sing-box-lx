//go:build with_gvisor

package wireguard

// lx:begin SPEC 052 netstack connect deadline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/link/channel"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv4"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
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

// TestDialTCPWithBind_deadlineCutsBlackholeConnect is the SPEC 052 regression:
// a connect into a silent blackhole must be cut by the ctx deadline instead of
// waiting out gVisor's full SYN backoff (~127s). The stack's NIC is a channel
// endpoint nobody reads — SYNs vanish, exactly a dead tunnel path. The parent
// ctx carries a short deadline so the test proves the ctx path works without
// sitting through the production 15s budget.
func TestDialTCPWithBind_deadlineCutsBlackholeConnect(t *testing.T) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	defer s.Close()
	ep := channel.New(8, 1500, "")
	if err := s.CreateNIC(1, ep); err != nil {
		t.Fatalf("CreateNIC: %s", err)
	}
	localAddr := tcpip.AddrFrom4([4]byte{10, 99, 0, 2})
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: localAddr, PrefixLen: 32},
	}, stack.AddressProperties{}); err != nil {
		t.Fatalf("AddProtocolAddress: %s", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := DialTCPWithBind(ctx, s, tcpip.FullAddress{}, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4([4]byte{203, 0, 113, 5}),
		Port: 80,
	}, ipv4.ProtocolNumber)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a blackholed connect must fail")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the failure must be the ctx deadline (classifiable), got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the deadline must cut the connect promptly, took %v", elapsed)
	}
}

// lx:end SPEC 052
