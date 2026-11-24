//go:build with_lx_command

package group

import (
	"testing"
	"time"

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
	if f.s.balancer.heal.running.Load() || f.s.balancer.heal.lastCheckEnd.Load() != nil || f.probes("a") != 0 {
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
