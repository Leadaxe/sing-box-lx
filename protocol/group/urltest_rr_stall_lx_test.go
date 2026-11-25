// lx:begin SPEC 138 round_robin: silence is no answer, network back after a stall,
// the control probed with the checked nodes and bracketing their silence

package group

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxhandshake"
	N "github.com/sagernet/sing/common/network"
)

func shortProbes(t *testing.T, timeout time.Duration) {
	t.Helper()
	original := rrProbeTimeoutNs.Swap(int64(timeout))
	t.Cleanup(func() { rrProbeTimeoutNs.Store(original) })
}

// (а) A silent failure (a timeout) counts against the site only when the open went
// to the site itself (a direct or endpoint leaf) and the node proved alive while
// that connection waited — decided at the failure, never later. A timeout of an
// open through a proxy leaf never does: the open does not reach the site. An active
// refusal waits for the verdict as before.
func TestRRSilence_decidedAtTheFailure(t *testing.T) {
	cases := []struct {
		name      string
		direct    bool
		err       error
		proof     time.Duration // the proof mark relative to the start of the connection; 0 none
		wantShift int
		pending   int32
	}{
		{"proxy, handshake timeout, no proof", false, lxhandshake.ErrTimeout, 0, 0, 0},
		{"proxy, deadline, proof before the wait", false, context.DeadlineExceeded, -time.Second, 0, 0},
		{"proxy, handshake timeout, proof during the wait", false, lxhandshake.ErrTimeout, 100 * time.Millisecond, 0, 0},
		{"direct, timeout, proof during the wait", true, context.DeadlineExceeded, 100 * time.Millisecond, 1, 0},
		{"proxy, refused: an answer, waits for the verdict", false, syscall.ECONNREFUSED, 0, 0, 1},
	}
	for _, c := range cases {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		f.fb["a"].direct = c.direct
		f.fb["a"].set(&f.fb["a"].openFail, "blocked.example", true)
		gap := time.Now()
		f.s.balancer.heal.lastCheckEnd.Store(&gap) // no probe: only the rule decides
		track := f.s.balancer.trackOf("a")
		track.verdictLast.Store(rrClock(gap))
		key := f.keyOn(t, "blocked.example", 0)
		start := time.Now().Add(-2 * time.Second)
		if c.proof != 0 {
			track.markProved(rrClock(start.Add(c.proof)))
		}
		conn, _ := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("blocked.example"), f.fb["a"], c.err, f.pickAt(key, "blocked.example", start))
		closeConn(conn)
		if shift, _ := f.shift(key, "blocked.example"); shift != c.wantShift {
			t.Fatalf("%s: shift %d, want %d", c.name, shift, c.wantShift)
		}
		if track.pendingN.Load() != c.pending {
			t.Fatalf("%s: pending %d, want %d", c.name, track.pendingN.Load(), c.pending)
		}
		// A later proof settles no silent failure.
		f.dial(t, f.keyOn(t, "open.example", 0), "open.example")
		if shift, _ := f.shift(key, "blocked.example"); c.err != syscall.ECONNREFUSED && shift != c.wantShift {
			t.Fatalf("%s: a later proof moved the key to %d", c.name, shift)
		}
		if c.err != syscall.ECONNREFUSED && c.wantShift == 0 && f.fails("a") != 0 {
			t.Fatalf("%s: a silent failure without proof counts nothing", c.name)
		}
	}
}

// (б) Reproduction of the stand (i1.log): every node stalls under sticky load; the
// check from a long wait starts its probes; the network comes back before their
// timeout and they answer; then the handshakes of the connections opened during the
// stall time out, and fresh connections flow again. No shift, no counted failure, no
// quarantine: the self-heal froze as the network came back.
func TestRRStall_networkBackFreezes(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	release := make(chan struct{})
	hold := make(chan struct{})
	var dials []chan dialResult
	for slot, tag := range []string{"a", "b", "c"} {
		node := f.fb[tag]
		node.set(&node.stall, "stall.example", true)
		node.access.Lock()
		node.release = release
		node.access.Unlock()
		node.hold = hold
		for i := 0; i < 3; i++ {
			dials = append(dials, f.dialAsync(f.keyOn(t, "stall.example", slot), "stall.example"))
		}
	}
	for _, tag := range []string{"a", "b", "c"} {
		track := f.s.balancer.trackOf(tag)
		waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	}
	time.Sleep(20 * time.Millisecond)
	for _, tag := range []string{"a", "b", "c"} {
		ageTrack(f.s.balancer.trackOf(tag), rrHangAfter+3*time.Second)
	}
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now().Add(-rrHangAfter - 3*time.Second)))
	// The look at a stalled slot starts the check from a long wait; its probes hang.
	f.s.selectBalanced(f.keyOn(t, "stall.example", 0), N.NetworkTCP, siteAddr("stall.example"))
	waitUntil(t, func() bool { return f.probes("a") == 1 && f.s.balancer.heal.running.Load() })
	// The network comes back: the probes, started during the stall, answer late.
	time.Sleep(rrFastAnswer + 100*time.Millisecond)
	close(hold)
	waitUntil(t, func() bool { return f.s.balancer.heal.frozen() })
	// The handshakes of the connections opened during the stall time out.
	close(release)
	for _, done := range dials {
		if result := waitDial(t, done); result.err == nil {
			result.conn.Close()
		}
	}
	// Fresh connections flow again through every node.
	for slot := range []string{"a", "b", "c"} {
		f.dial(t, f.keyOn(t, "open.example", slot), "open.example")
	}
	f.waitCheck(t)
	if size := f.s.balancer.memory.shifts.size.Load(); size != 0 {
		t.Fatalf("no key shifts after the network came back, got %d", size)
	}
	for _, tag := range []string{"a", "b", "c"} {
		if f.fails(tag) != 0 || f.s.balancer.isQuarantined(tag) {
			t.Fatalf("%s: no counted failure and no quarantine", tag)
		}
	}
	if !samePool(f.pool(), "a", "b", "c") {
		t.Fatalf("nobody is struck, pool %v", f.pool())
	}
	if !f.s.balancer.heal.frozen() {
		t.Fatal("the network coming back freezes the self-heal")
	}
}

// (б) One stalled node while the others serve bytes is no stall of the network; nor
// is a pool of one.
func TestRRStall_oneNodeIsNoStall(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	var entry rrTrackEntry
	track := f.s.balancer.trackOf("a")
	track.track(&track.dials, &entry, rrClock(time.Now().Add(-rrHangAfter-time.Second)), nopKill{})
	f.s.balancer.heal.lastByte.Store(rrClock(time.Now().Add(-time.Minute)))
	f.dial(t, f.keyOn(t, "ok.example", 1), "ok.example")
	if f.s.balancer.heal.frozen() {
		t.Fatal("one stalled node is no stall of the network")
	}
	g := newShiftFixture(t, []string{"a", "b"}, []string{"a"})
	var only rrTrackEntry
	gt := g.s.balancer.trackOf("a")
	gt.track(&gt.dials, &only, rrClock(time.Now().Add(-rrHangAfter-time.Second)), nopKill{})
	g.dial(t, g.keyOn(t, "ok.example", 0), "ok.example")
	if g.s.balancer.heal.frozen() {
		t.Fatal("a pool of one is no stall of the network")
	}
}

// (в, г) Reproduction of the stand (d.log): A and B are silent for the whole probe
// window; the control C is silent at first and answers only at the end of the
// window, when the network came back. Nobody is struck: the control did not answer
// before the silence.
func TestRRBracket_lateControlStrikesNobody(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	down := make(chan struct{})
	f.fb["a"].hold, f.fb["b"].hold = down, down
	f.fb["c"].slow = 350 * time.Millisecond
	// The network comes back at the end of the first window: A and B answer from then.
	time.AfterFunc(rrProbeTimeout()+20*time.Millisecond, func() { close(down) })
	if !f.s.rrStartCheck(f.fb["a"], false) {
		t.Fatal("precondition: the check starts")
	}
	f.waitCheck(t)
	if f.probes("c") < 1 {
		t.Fatalf("the third is probed with A and B, c probed %d", f.probes("c"))
	}
	if !samePool(f.pool(), "a", "b", "c") || f.g.history.LoadURLTestHistory("a") == nil || f.g.history.LoadURLTestHistory("b") == nil {
		t.Fatalf("a control that answered only late strikes nobody: A and B answer the second round, pool %v", f.pool())
	}
	if f.probes("a") != 2 || f.probes("b") != 2 {
		t.Fatalf("A and B are probed again with the control: a %d b %d", f.probes("a"), f.probes("b"))
	}
}

// (в, г) A and B really are dead; the control answers before and after: both are
// struck out in one check. The time to the verdict for refused nodes stays at the
// probes plus one fast second probe of the control.
func TestRRBracket_deadPairStruckInOneCheck(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c"})
	f.fb["a"].dead.Store(true)
	f.fb["b"].dead.Store(true)
	start := time.Now()
	if !f.s.rrStartCheck(f.fb["a"], false) {
		t.Fatal("precondition: the check starts")
	}
	f.waitCheck(t)
	elapsed := time.Since(start)
	if f.g.history.LoadURLTestHistory("a") != nil || f.g.history.LoadURLTestHistory("b") != nil {
		t.Fatal("a and b are struck out in one check")
	}
	if f.probes("c") != 2 {
		t.Fatalf("the control answers before and after: c probed %d", f.probes("c"))
	}
	t.Logf("refused pair struck in %v", elapsed)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("refused nodes are struck without waiting for a timeout, took %v", elapsed)
	}
}

// (г) A silent for the whole window, the control answers at once and again after:
// A is struck after the timeout plus one fast probe. If A proves alive while the
// control is probed again, the verdict is dropped.
func TestRRBracket_silentNodeAndProofDuringSecondProbe(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	never := make(chan struct{})
	defer close(never)
	f.fb["a"].hold = never
	start := time.Now()
	f.s.rrStartCheck(f.fb["a"], false)
	f.waitCheck(t)
	elapsed := time.Since(start)
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("silent a is struck out")
	}
	t.Logf("silent node struck in %v (probe timeout %v)", elapsed, rrProbeTimeout())

	g := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	g.fb["a"].hold = never
	g.fb["b"].slow = 80 * time.Millisecond // a fast control: one more probe of it after A's silence
	checkStart := time.Now()
	g.s.rrStartCheck(g.fb["a"], false)
	time.AfterFunc(440*time.Millisecond, func() {
		// a first byte of a connection through a that started after a's probe
		g.s.balancer.trackOf("a").markProved(rrClock(checkStart.Add(50 * time.Millisecond)))
	})
	g.waitCheck(t)
	if g.g.history.LoadURLTestHistory("a") == nil || !samePool(g.pool(), "a", "b", "c") {
		t.Fatalf("a proved alive during the second probe of the control: no verdict, pool %v", g.pool())
	}
}

var _ = errors.Is

// lx:end SPEC 138
