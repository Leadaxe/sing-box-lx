//go:build with_lx_command

package group

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 139 §5.7 — a connection test reads the group state but never writes
// it: a failed probe dial leaves penalties, history and the selection as they
// were, while still taking the production fallback path. Mirrors
// TestPenaltyFailoverDial.

func probeTrace() (context.Context, *lxtrace.Trace) {
	trace := lxtrace.New(lxtrace.Options{Probe: true})
	return lxtrace.With(context.Background(), trace), trace
}

func TestProbeDialLeavesPenaltiesUntouched_LX(t *testing.T) {
	dead := &penaltyDialNode{tag: "dead", err: context.DeadlineExceeded}
	alive := &penaltyDialNode{tag: "alive", addr: "server"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{dead, alive}, map[string]uint16{"dead": 10, "alive": 50})
	g.groupTag = "auto"
	g.selectedOutboundTCP = dead
	s := &URLTest{outbound: g.outbound, logger: log.NewNOPFactory().NewLogger("test"), group: g}

	ctx, trace := probeTrace()
	conn, err := s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal("the probe must take the fallback path: ", err)
	}
	conn.Close()
	if alive.dials != 1 {
		t.Fatalf("the probe still dials the fallback once, got %d", alive.dials)
	}
	if g.penaltyOf("dead") != 0 || g.penaltyOf("alive") != 0 {
		t.Fatalf("a probe must not touch penalties: dead=%d alive=%d", g.penaltyOf("dead"), g.penaltyOf("alive"))
	}
	if g.history.LoadURLTestHistory("dead") == nil {
		t.Fatal("a probe must not delete the failed node's history")
	}
	if g.selectedOutboundTCP != adapter.Outbound(dead) {
		t.Fatal("a probe must not move the selection")
	}

	summary := trace.Finish(nil)
	var groups []lxtrace.StageSnapshot
	for _, stage := range summary.Stages {
		if stage.Kind == "group" {
			groups = append(groups, stage)
		}
	}
	if len(groups) != 2 {
		t.Fatalf("want the pick and the fallback pick as group stages, got %+v", summary.Stages)
	}
	if groups[0].Subject != "auto" || groups[0].Result != lxtrace.OK ||
		stageAttr(groups[0], "mode") != C.URLTestModeLeastTest || stageAttr(groups[0], "selected") != "dead" {
		t.Fatalf("unexpected pick stage: %+v", groups[0])
	}
	if stageAttr(groups[1], "selected") != "alive" || stageAttr(groups[1], "fallback_for") != "dead" {
		t.Fatalf("unexpected fallback stage: %+v", groups[1])
	}

	// Control: the same failure without a probe applies the SPEC 054 side effects.
	alive.dials = 0
	conn, err = s.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if g.penaltyOf("dead") != 1 || g.history.LoadURLTestHistory("dead") != nil || g.selectedOutboundTCP != adapter.Outbound(alive) {
		t.Fatal("a production dial must penalize, drop the history and move the selection")
	}
}

func TestProbeRoundRobinDoesNotRotate_LX(t *testing.T) {
	b := &balancer{poolSize: 2}
	b.setSlots([]string{"a", "b"})
	ctx, _ := probeTrace()
	before := b.counter.Load()
	b.rotate(ctx)
	b.rotate(ctx)
	if b.counter.Load() != before {
		t.Fatal("a probe must not advance the round_robin rotation")
	}
	b.rotate(context.Background())
	if b.counter.Load() != before+1 {
		t.Fatal("a production pick advances the rotation")
	}
}

func stageAttr(stage lxtrace.StageSnapshot, key string) string {
	for _, a := range stage.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}
