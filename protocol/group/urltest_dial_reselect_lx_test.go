package group

import (
	"context"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// SPEC 166 — since upstream 37fffa325 a failed dial in URLTest.DialContext
// erases the leaf's history and re-selects at once (PerformUpdateCheck, which
// interrupts the group's connections when the selection changes). Every routed
// connection of ours goes through this code (SPEC 140), so the re-select must
// not take over what SPEC 054 owns: a dead path moves the selection itself,
// through the fallback that proved alive, without Interrupt.

type reselectCloser struct{ closed atomic.Bool }

func (c *reselectCloser) Close() error {
	c.closed.Store(true)
	return nil
}

func newReselectTest(t *testing.T, nodes []*penaltyDialNode, delays map[string]uint16) (*URLTest, *URLTestGroup, *reselectCloser) {
	t.Helper()
	g := newPenaltyTestGroup(t, nodes, delays)
	closer := &reselectCloser{}
	g.interruptGroup.Add(closer, false)
	s := &URLTest{outbound: g.outbound, logger: log.NewNOPFactory().NewLogger("test"), group: g}
	return s, g, closer
}

func TestDialDeadPathMovesSelectionWithoutInterrupt(t *testing.T) {
	dead := &penaltyDialNode{tag: "dead", err: syscall.EHOSTUNREACH, typ: "socks"}
	alive := &penaltyDialNode{tag: "alive", addr: "server"}
	s, g, closer := newReselectTest(t, []*penaltyDialNode{dead, alive}, map[string]uint16{"dead": 10, "alive": 50})
	g.selectedOutboundTCP.Store(dead)

	conn, err := s.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal("the fallback dial must carry the connection: ", err)
	}
	conn.Close()
	if g.selectedOutboundTCP.Load() != adapter.Outbound(alive) {
		t.Fatal("the selection must move to the fallback")
	}
	if closer.closed.Load() {
		t.Fatal("SPEC 054 moves the selection without Interrupt; the upstream re-select must not run on a dead path")
	}
	if g.penaltyOf("dead") != 1 {
		t.Fatalf("the dead node gets its penalty, got %d", g.penaltyOf("dead"))
	}
	if g.history.LoadURLTestHistory("dead") != nil {
		t.Fatal("the dead node's history goes, as upstream")
	}
}

// A refused open through a direct node is the destination's answer: SPEC 054
// takes no fallback, and the upstream reaction stays — the history goes and the
// group re-selects at once.
func TestDialDestinationRefusedReselectsAsUpstream(t *testing.T) {
	site := &penaltyDialNode{tag: "site", err: syscall.ECONNREFUSED}
	other := &penaltyDialNode{tag: "other", addr: "server"}
	s, g, closer := newReselectTest(t, []*penaltyDialNode{site, other}, map[string]uint16{"site": 10, "other": 50})
	g.selectedOutboundTCP.Store(site)

	_, err := s.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err == nil {
		t.Fatal("a refused destination is an error")
	}
	if other.dials != 0 {
		t.Fatalf("no fallback dial for a refused destination, got %d", other.dials)
	}
	if g.penaltyOf("site") != 0 {
		t.Fatal("a refused destination is no penalty")
	}
	if g.history.LoadURLTestHistory("site") != nil {
		t.Fatal("upstream erases the history on a refused dial")
	}
	if g.selectedOutboundTCP.Load() != adapter.Outbound(other) || !closer.closed.Load() {
		t.Fatal("upstream re-selects at once and interrupts the group's connections")
	}
}

// The router calls PerformUpdateCheck of every urltest group of the chain on a
// failed open (route/conn.go). round_robin keeps no single selection: it
// re-selects nothing and interrupts nothing.
func TestRoundRobinPerformUpdateCheckIsNoop(t *testing.T) {
	a := &penaltyDialNode{tag: "a", addr: "server"}
	b := &penaltyDialNode{tag: "b", addr: "server"}
	s, g, closer := newReselectTest(t, []*penaltyDialNode{a, b}, map[string]uint16{"a": 50, "b": 10})
	g.balancer = &balancer{poolSize: 2}
	g.selectedOutboundTCP.Store(a)

	s.PerformUpdateCheck()
	if g.selectedOutboundTCP.Load() != adapter.Outbound(a) || closer.closed.Load() {
		t.Fatal("round_robin must not re-select or interrupt on PerformUpdateCheck")
	}
}
