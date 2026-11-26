package trafficcontrol

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 138 — the connection list shows the node a per-connection group
// picked for that connection, not the group's last pick.

type actualNode struct {
	adapter.Outbound
	tag string
}

func (o *actualNode) Tag() string                        { return o.tag }
func (o *actualNode) Type() string                       { return C.TypeDirect }
func (o *actualNode) Dependencies() []string             { return nil }
func (o *actualNode) Network() []string                  { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *actualGroup) Type() string                      { return C.TypeURLTest }
func (o *actualGroup) All() []string                     { return nil }
func (o *actualGroup) Selected(string) adapter.Outbound  { return o.selected }
func (o *actualGroup) AttachConnection(io.Closer) func() { return func() {} }

// actualGroup points at selected: the round_robin "last selected", or a
// selector's choice.
type actualGroup struct {
	actualNode
	selected adapter.Outbound
}

func newActualGroup(tag string, selected adapter.Outbound) *actualGroup {
	return &actualGroup{actualNode: actualNode{tag: tag}, selected: selected}
}

func routedActual(t *testing.T, m *Manager, chain ...adapter.Outbound) (*adapter.ActualOutboundLx, Tracker) {
	t.Helper()
	cell := new(adapter.ActualOutboundLx)
	metadata := adapter.InboundContext{Network: N.NetworkTCP, OutboundChain: chain, ActualOutboundLx: cell}
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	conn := m.RoutedConnection(context.Background(), client, metadata, nil, chain[0])
	return cell, conn.(Tracker)
}

func assertShown(t *testing.T, tracker Tracker, chain ...string) {
	t.Helper()
	metadata := tracker.Metadata()
	if len(metadata.Chain) != len(chain) {
		t.Fatalf("chain = %v, want %v", metadata.Chain, chain)
	}
	for i := range chain {
		if metadata.Chain[i] != chain[i] {
			t.Fatalf("chain = %v, want %v", metadata.Chain, chain)
		}
	}
	if metadata.Outbound != chain[0] {
		t.Fatalf("outbound = %q, want %q", metadata.Outbound, chain[0])
	}
}

func TestActualNodeTwoConnectionsTwoSlots_LX(t *testing.T) {
	a, b := &actualNode{tag: "a"}, &actualNode{tag: "b"}
	rr := newActualGroup("rr", b)
	m := NewManager()

	cell1, conn1 := routedActual(t, m, rr)
	cell2, conn2 := routedActual(t, m, rr)
	// before the dial the entry keeps what the group pointed at on creation
	assertShown(t, conn1, "b", "rr")
	if conn1.Metadata() != conn1.Metadata() || conn1.Metadata().IsActualViewLx() {
		t.Fatal("without a pick Metadata must stay the base entry")
	}

	cell1.Pick("rr", a)
	cell2.Pick("rr", b)
	rr.selected = b // round_robin's last pick: the second connection's node
	assertShown(t, conn1, "a", "rr")
	assertShown(t, conn2, "b", "rr")
	if conn1.Metadata().OutboundType != C.TypeDirect {
		t.Fatalf("outbound type = %q", conn1.Metadata().OutboundType)
	}
	view := conn1.Metadata()
	if view != conn1.Metadata() || !view.IsActualViewLx() {
		t.Fatal("one view per state: a reader must get the same copy until the cell changes")
	}

	// a retry through the neighbour slot replaces the group's pick
	cell1.Pick("rr", b)
	assertShown(t, conn1, "b", "rr")
	if view.Chain[0] != "a" || view.IsActualViewLx() {
		t.Fatal("an older view must stay as it was and stop being current")
	}

	// the closed entry keeps the node the connection went through
	cell2.Pick("rr", a)
	conn2.Close()
	closed := m.ClosedConnections()
	if len(closed) != 1 || closed[0].Chain[0] != "a" || closed[0].Outbound != "a" {
		t.Fatalf("closed entry = %+v", closed)
	}
}

func TestActualNodeNestedGroups_LX(t *testing.T) {
	a, b := &actualNode{tag: "a"}, &actualNode{tag: "b"}
	inner := newActualGroup("inner", b)
	outer := newActualGroup("outer", b)
	selector := newActualGroup("proxy", outer)
	m := NewManager()

	cell, conn := routedActual(t, m, selector, outer)
	cell.Pick("outer", inner)
	// the inner group has not dialed yet: it shows what it points at
	assertShown(t, conn, "b", "inner", "outer", "proxy")
	cell.Pick("inner", a)
	assertShown(t, conn, "a", "inner", "outer", "proxy")
	// a retry of the outer group to a plain node drops the inner group from view
	cell.Pick("outer", b)
	assertShown(t, conn, "b", "outer", "proxy")
}

func TestActualNodeSelectorUnchanged_LX(t *testing.T) {
	a, b := &actualNode{tag: "a"}, &actualNode{tag: "b"}
	selector := newActualGroup("proxy", a)
	m := NewManager()
	// a selector is resolved by the router to its node and never writes the cell
	_, conn := routedActual(t, m, selector, a)
	assertShown(t, conn, "a", "proxy")
	selector.selected = b
	assertShown(t, conn, "a", "proxy")

	// least_test / failover: the router stops at the group, the group writes
	// nothing, the entry keeps the node the group pointed at on creation
	leastTest := newActualGroup("auto", a)
	_, conn = routedActual(t, m, leastTest)
	leastTest.selected = b
	assertShown(t, conn, "a", "auto")
}

func TestActualNodeConcurrentReaders_LX(t *testing.T) {
	a, b := &actualNode{tag: "a"}, &actualNode{tag: "b"}
	rr := newActualGroup("rr", a)
	m := NewManager()
	cell, conn := routedActual(t, m, rr)
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 1000 {
				metadata := conn.Metadata()
				if metadata.Outbound != metadata.Chain[0] {
					t.Error("torn view: ", metadata.Outbound, " ", metadata.Chain)
					return
				}
			}
		}()
	}
	for i := range 1000 {
		if i%2 == 0 {
			cell.Pick("rr", a)
		} else {
			cell.Pick("rr", b)
		}
	}
	wait.Wait()
}
