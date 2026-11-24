//go:build with_lx_command

package group

import (
	"testing"
	"time"

	"github.com/sagernet/sing/common"
)

// lx: SPEC 138 §3.8/§3.9 — a connection test (SPEC 139) over UDP gets the retry,
// with both picks in the trace, and changes nothing: no check, no counter, no
// "network alive" mark, history and pool as they were; its flow is not watched.
func TestRRUDP_connectionTestRetriesRecordsNothing_LX(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.g.groupTag = "auto"
	f.udp["a"].dead.Store(true)

	ctx, trace := probeTrace()
	conn, err := f.s.ListenPacket(ctx, udpSite())
	if err != nil {
		t.Fatal("the connection test must take the retry: ", err)
	}
	if _, wrapped := common.Cast[*rrFirstPacketConn](conn); wrapped {
		t.Fatal("a connection test is not watched")
	}
	exchange(t, conn)
	conn.Close()
	time.Sleep(20 * time.Millisecond)
	if f.listens("b") != 1 {
		t.Fatal("the retry goes through b")
	}
	if f.checkStarted() || f.probes("a") != 0 {
		t.Fatal("a connection test must not start a check")
	}
	if f.g.history.LoadURLTestHistory("a") == nil || !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("a connection test must not touch history or pool, got %v", f.pool())
	}
	b := f.s.balancer
	b.memory.access.Lock()
	counted := len(b.memory.counts)
	b.memory.access.Unlock()
	if counted != 0 || provedAny(b) || b.memory.shifts.size.Load() != 0 {
		t.Fatalf("a connection test records nothing: counts %d proved %v", counted, provedAny(b))
	}
	var selected, fallbackFor []string
	for _, stage := range trace.Finish(nil).Stages {
		if stage.Kind == "group" {
			selected = append(selected, stageAttr(stage, "selected"))
			fallbackFor = append(fallbackFor, stageAttr(stage, "fallback_for"))
		}
	}
	if len(selected) != 2 || selected[0] != "a" || selected[1] != "b" || fallbackFor[1] != "a" {
		t.Fatalf("want the pick and the retry as group stages, got %v / %q", selected, fallbackFor)
	}
}
