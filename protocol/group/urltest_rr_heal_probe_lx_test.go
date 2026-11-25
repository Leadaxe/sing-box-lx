//go:build with_lx_command

package group

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 138 §3.9 — a connection test (SPEC 139) gets the round_robin retry, with
// both picks in the trace, but starts no check and leaves history and pool alone.
func TestRRHeal_connectionTestRetriesWithoutCheck_LX(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"}, 0)
	f.g.groupTag = "auto"
	f.s.balancer.stickyHash = nil
	f.nodes["a"].dead.Store(true)

	ctx, trace := probeTrace()
	conn, err := f.s.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPort("example.org", 443))
	if err != nil {
		t.Fatal("the connection test must take the retry: ", err)
	}
	conn.Close()
	time.Sleep(20 * time.Millisecond)
	if f.s.balancer.heal.roundBusy() || f.s.balancer.heal.pauseEnd() != nil || f.probes("a") != 0 {
		t.Fatal("a connection test must not start a check")
	}
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a connection test must not touch history or pool, got %v", f.pool())
	}
	if got := f.g.lastSelected.Load(); got != "" {
		t.Fatalf("a connection test must not move Now(), got %q", got)
	}
	var selected, fallbackFor []string
	for _, stage := range trace.Finish(nil).Stages {
		if stage.Kind == "group" {
			selected = append(selected, stageAttr(stage, "selected"))
			fallbackFor = append(fallbackFor, stageAttr(stage, "fallback_for"))
		}
	}
	if len(selected) != 2 || selected[0] != "a" || selected[1] != "b" {
		t.Fatalf("want the pick and the retry as group stages, got %v", selected)
	}
	if fallbackFor[0] != "" || fallbackFor[1] != "a" {
		t.Fatalf("the retry stage must carry fallback_for=a, the pick none, got %q", fallbackFor)
	}
}

// lx: SPEC 138 stage 2 — a connection test writes no shift, no counter and no
// "network alive" mark, and its connection is not watched.
func TestRRShift_connectionTestRecordsNothing_LX(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.g.groupTag = "auto"
	f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
	key := f.keyOn(t, "blocked.example", 0)
	ctx, _ := probeTrace()
	ctx = adapter.WithContext(ctx, adapter.ContextFrom(key))
	conn, err := f.s.DialContext(ctx, N.NetworkTCP, siteAddr("blocked.example"))
	if err != nil {
		t.Fatal("the connection test takes the retry: ", err)
	}
	if _, wrapped := common.Cast[*rrFirstByteConn](conn); wrapped {
		t.Fatal("a connection test is not watched")
	}
	conn.Write([]byte("hi"))
	buffer := make([]byte, 8)
	conn.Read(buffer)
	conn.Close()
	time.Sleep(20 * time.Millisecond)
	b := f.s.balancer
	b.memory.access.Lock()
	counted := len(b.memory.counts)
	b.memory.access.Unlock()
	if b.memory.shifts.size.Load() != 0 || counted != 0 || provedAny(b) {
		t.Fatalf("a connection test records nothing: shifts %d counts %d proved %v", b.memory.shifts.size.Load(), counted, provedAny(b))
	}
	if b.trackOf("a").pendingN.Load() != 0 || b.trackOf("b").pendingN.Load() != 0 {
		t.Fatal("a connection test leaves no Q1 sign")
	}
}

// lx: SPEC 138 — the dial of a connection test is not tracked as unfinished: the
// dead verdict does not cancel it, and a pick by it does not look at the slot.
func TestRRTrack_connectionTestNotTracked_LX(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	key := f.keyOn(t, "site.example", 0)
	ctx, _ := probeTrace()
	ctx = adapter.WithContext(ctx, adapter.ContextFrom(key))
	selected, picked := f.s.selectBalanced(ctx, N.NetworkTCP, siteAddr("site.example"))
	if selected == nil || picked.track == nil {
		t.Fatal("precondition: a pick from the pool")
	}
	if dialCtx := f.s.rrDialStart(ctx, selected, &picked); dialCtx.Done() != ctx.Done() || picked.dial != nil || !rrInnerDial(dialCtx) {
		t.Fatal("a connection test dials on its own context (only marked as a round_robin open), untracked")
	}
	plain := f.keyOn(t, "site.example", 0)
	_, picked = f.s.selectBalanced(plain, N.NetworkTCP, siteAddr("site.example"))
	if dialCtx := f.s.rrDialStart(plain, selected, &picked); dialCtx == plain || picked.dial == nil {
		t.Fatal("precondition: a production dial is tracked")
	}
	picked.dial.failed()
}
