package group

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 138 — a round_robin pick lands in the connection's cell, where the
// connection list reads it; least_test leaves the cell alone.

func actualContext() (context.Context, *adapter.ActualOutboundLx) {
	cell := new(adapter.ActualOutboundLx)
	return adapter.WithContext(context.Background(), &adapter.InboundContext{ActualOutboundLx: cell}), cell
}

func TestBalancedPickRecordsActualNode_LX(t *testing.T) {
	a := &penaltyDialNode{tag: "a"}
	b := &penaltyDialNode{tag: "b"}
	g := &URLTestGroup{lxURLTestGroup: lxURLTestGroup{groupTag: "rr"}}

	ctx1, cell1 := actualContext()
	ctx2, cell2 := actualContext()
	g.balancedPicked(ctx1, a)
	g.balancedPicked(ctx2, b)
	if cell1.Load().Picked("rr") != adapter.Outbound(a) || cell2.Load().Picked("rr") != adapter.Outbound(b) {
		t.Fatal("each connection must carry its own pick")
	}
	// a retry through another slot replaces the pick
	g.balancedPicked(ctx1, b)
	if cell1.Load().Picked("rr") != adapter.Outbound(b) {
		t.Fatal("the retry pick must replace the first one")
	}
	// the copy an outbound makes of the metadata shares the cell
	extended, _ := adapter.ExtendContext(ctx2)
	g.balancedPicked(extended, a)
	if cell2.Load().Picked("rr") != adapter.Outbound(a) {
		t.Fatal("an ExtendContext copy must write into the same cell")
	}
	// no connection metadata: nothing to write, nothing breaks
	g.balancedPicked(context.Background(), a)
}

func TestLeastTestDialLeavesActualCellEmpty_LX(t *testing.T) {
	alive := &penaltyDialNode{tag: "alive", addr: "server"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{alive}, map[string]uint16{"alive": 10})
	g.groupTag = "auto"
	s := &URLTest{outbound: g.outbound, group: g}
	ctx, cell := actualContext()
	conn, err := s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if cell.Load() != nil {
		t.Fatal("least_test must not write the cell: the list keeps Selected()")
	}
}
