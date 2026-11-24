package daemon

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
)

// lx: SPEC 138 — a connection announced with the node its group picked is not
// announced again on the next tick: the tick carries its traffic delta.

type actualTestNode struct {
	adapter.Outbound
	tag string
}

func (o *actualTestNode) Tag() string            { return o.tag }
func (o *actualTestNode) Type() string           { return C.TypeDirect }
func (o *actualTestNode) Dependencies() []string { return nil }
func (o *actualTestNode) Network() []string      { return []string{N.NetworkTCP} }

type actualTestGroup struct {
	actualTestNode
	selected adapter.Outbound
}

func (o *actualTestGroup) Type() string                      { return C.TypeURLTest }
func (o *actualTestGroup) All() []string                     { return nil }
func (o *actualTestGroup) Selected(string) adapter.Outbound  { return o.selected }
func (o *actualTestGroup) AttachConnection(io.Closer) func() { return func() {} }

func pickedConnection(t *testing.T) (*trafficcontrol.Manager, *trafficcontrol.TrackerMetadata) {
	t.Helper()
	a, b := &actualTestNode{tag: "a"}, &actualTestNode{tag: "b"}
	rr := &actualTestGroup{actualTestNode: actualTestNode{tag: "rr"}, selected: b}
	m := trafficcontrol.NewManager()
	cell := new(adapter.ActualOutboundLx)
	metadata := adapter.InboundContext{Network: N.NetworkTCP, OutboundChain: []adapter.Outbound{rr}, ActualOutboundLx: cell}
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	conn := m.RoutedConnection(context.Background(), client, metadata, nil, rr)
	cell.Pick("rr", a)
	view := conn.(trafficcontrol.Tracker).Metadata()
	if !view.IsActualViewLx() || view.Outbound != "a" {
		t.Fatalf("precondition: the view shows the pick, got %q", view.Outbound)
	}
	return m, view
}

func assertDeltaNotNew(t *testing.T, events []*ConnectionEvent, how string) {
	t.Helper()
	if len(events) != 1 || events[0].Type != ConnectionEventType_CONNECTION_EVENT_UPDATE || events[0].UplinkDelta != 100 {
		t.Fatalf("%s: the tick must carry the delta, not a repeated NEW: %+v", how, events)
	}
}

func TestActualViewAnnouncedOnce_LX(t *testing.T) {
	s := &StartedService{}

	// a subscriber that connects after the pick: the initial state carries it
	m, view := pickedConnection(t)
	snapshots := make(map[uuid.UUID]connectionSnapshot)
	initial := s.buildInitialConnectionState(m, snapshots)
	if len(initial) != 1 || initial[0].Connection.Outbound != "a" {
		t.Fatalf("initial NEW must carry the pick: %+v", initial)
	}
	view.Upload.Add(100)
	assertDeltaNotNew(t, s.buildTrafficUpdates(m, snapshots), "initial state")

	// a connection first seen by the tick
	m, view = pickedConnection(t)
	snapshots = make(map[uuid.UUID]connectionSnapshot)
	if first := s.buildTrafficUpdates(m, snapshots); len(first) != 1 || first[0].Type != ConnectionEventType_CONNECTION_EVENT_NEW {
		t.Fatalf("first tick announces the connection: %+v", first)
	}
	view.Upload.Add(100)
	assertDeltaNotNew(t, s.buildTrafficUpdates(m, snapshots), "first seen by the tick")

	// a NEW event that already carries the pick
	m, view = pickedConnection(t)
	snapshots = make(map[uuid.UUID]connectionSnapshot)
	if event := s.applyConnectionEvent(trafficcontrol.ConnectionEvent{Type: trafficcontrol.ConnectionEventNew, ID: view.ID, Metadata: view}, snapshots); event == nil {
		t.Fatal("the NEW event is forwarded")
	}
	view.Upload.Add(100)
	assertDeltaNotNew(t, s.buildTrafficUpdates(m, snapshots), "NEW event")
}
