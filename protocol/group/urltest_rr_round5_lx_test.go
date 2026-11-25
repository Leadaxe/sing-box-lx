// lx:begin SPEC 138 round_robin: a silent site through a live node, the sticky
// suspicion after silence, no gaps across a freeze, a stall counts unproved nodes

package group

import (
	"context"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

// openHole opens a connection of the key ctx to host (mute: the node opens it, the
// site never answers) and has the client send.
func (f *shiftFixture) openHole(t *testing.T, ctx context.Context, host string) (net.Conn, *rrFirstByteConn) {
	t.Helper()
	conn, err := f.s.DialContext(ctx, N.NetworkTCP, siteAddr(host))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.Write([]byte("GET /"))
	inner, _ := common.Cast[*rrFirstByteConn](conn)
	if inner == nil {
		t.Fatal("precondition: a watched connection")
	}
	return conn, inner
}

// agedWait moves the wait of the connection rrHangAfter and more into the past.
func agedWait(track *rrTrack, inner *rrFirstByteConn) {
	ageTrack(track, rrHangAfter+time.Second)
	inner.wroteAt.Add(-int64(rrHangAfter + time.Second))
}

// 1. Reproduction of hole_1.log: the site holds the connection with no answer while
// the node serves other requests. While the client waits nothing moves (a slow
// answer is no silence); the client gives up after 8 s — the key moves on and the
// next request goes through another node. Nobody is checked or struck.
func TestRRSiteSilence_liveNodeUnderLoad(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	hole, inner := f.openHole(t, key, "hole.example")
	track := f.s.balancer.trackOf("a")
	// The load: another key of slot a gets its answer after the hole opened.
	if _, answered := f.dial(t, f.keyOn(t, "ok.example", 0), "ok.example"); !answered {
		t.Fatal("precondition: a serves")
	}
	agedWait(track, inner)
	f.s.selectBalanced(f.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example")) // a look at slot a
	if shift, _ := f.shift(key, "hole.example"); shift != 0 {
		t.Fatalf("a wait of 6 s is a slow answer, not silence: got shift %d", shift)
	}
	inner.wroteAt.Add(-int64(2 * time.Second)) // the client gives up after 8 s
	hole.Close()
	if shift, _ := f.shift(key, "hole.example"); shift != 1 {
		t.Fatalf("the client left with no byte through live a: the key moves on, got shift %d", shift)
	}
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("hole.example")); selected.Tag() != "b" {
		t.Fatalf("the next request of the key goes through b, got %s", selected.Tag())
	}
	if f.probes("a") != 0 || !samePool(f.pool(), "a", "b", "c") {
		t.Fatal("a is not checked or struck")
	}
}

// 1. Reproduction of hole_0.log: nothing else goes through the node. Its slot is
// suspect, the check probes it, it answers while the connection waits; the client
// gives up after 8 s: the key moves on.
func TestRRSiteSilence_idleNodeProbeAnswers(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	hole, inner := f.openHole(t, key, "hole.example")
	agedWait(f.s.balancer.trackOf("a"), inner)
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("hole.example")); selected.Tag() == "a" {
		t.Fatal("precondition: idle a is suspect")
	}
	f.waitCheck(t)
	if f.probes("a") == 0 || f.g.history.LoadURLTestHistory("a") == nil {
		t.Fatal("the check probes a, a answers")
	}
	if shift, _ := f.shift(key, "hole.example"); shift != 0 {
		t.Fatalf("the probe's answer alone moves no key, got %d", shift)
	}
	inner.wroteAt.Add(-int64(2 * time.Second))
	hole.Close()
	if shift, _ := f.shift(key, "hole.example"); shift != 1 {
		t.Fatalf("a answered while the connection waited and the client left: the key moves on, got %d", shift)
	}
}

// 2. A slow answer — 6, 10, 20 s — is a success: nothing moves while it waits, at
// the looks or after a probe, nor when the answer comes and the connection ends.
// A wait over rrSiteSilentAfter while the client still waits is silence.
func TestRRSiteSilence_slowAnswerIsNoSilence(t *testing.T) {
	for _, wait := range []time.Duration{6 * time.Second, 10 * time.Second, 20 * time.Second} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		key := f.keyOn(t, "slow.example", 0)
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		track := f.s.balancer.trackOf("a")
		start := time.Now()
		conn := newRRFirstByteConn(client, &rrConnWatch{s: f.s, member: "a", track: track, start: start, key: f.s.balancer.rrKeyOf(key, siteAddr("slow.example"))})
		conn.Write([]byte("GET /slow"))
		track.markProved(rrClock(start.Add(time.Millisecond)))
		ageTrack(track, wait)
		conn.wroteAt.Add(-int64(wait))
		track.hangLook.Store(0)
		f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
		f.s.rrSettleProbe(rrProbe{member: "a", leaf: "a", answered: true, started: time.Now(), ended: time.Now()})
		go server.Write([]byte("HTTP/1.1 200"))
		buffer := make([]byte, 16)
		if n, _ := conn.Read(buffer); n == 0 {
			t.Fatal("precondition: the answer")
		}
		conn.Close()
		server.Close()
		if shift, _ := f.shift(key, "slow.example"); shift != 0 {
			t.Fatalf("an answer after %v is a success: no shift, got %d", wait, shift)
		}
	}
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	_, inner := f.openHole(t, key, "hole.example")
	track := f.s.balancer.trackOf("a")
	track.markProved(rrClock(time.Now()))
	ageTrack(track, rrSiteSilentAfter+time.Second)
	inner.wroteAt.Add(-int64(rrSiteSilentAfter + time.Second))
	track.hangLook.Store(0)
	f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
	if shift, _ := f.shift(key, "hole.example"); shift != 1 {
		t.Fatalf("a wait of 31 s with no byte through live a is silence, got %d", shift)
	}
}

// 1. Reverse: the node proved nothing while the connection waited — no shift, the
// slot is suspect as before.
func TestRRSiteSilence_noProofNoShift(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].mute, "hole.example", true)
	key := f.keyOn(t, "hole.example", 0)
	_, inner := f.openHole(t, key, "hole.example")
	track := f.s.balancer.trackOf("a")
	agedWait(track, inner)
	f.hold("a")
	f.s.balancer.heal.running.Store(true) // no check now
	defer f.s.balancer.heal.running.Store(false)
	f.s.selectBalanced(f.keyOn(t, "ok.example", 0), N.NetworkTCP, siteAddr("ok.example"))
	track.hangLook.Store(0)
	f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
	if shift, _ := f.shift(key, "hole.example"); shift != 0 {
		t.Fatalf("no proof during the wait: no shift, got %d", shift)
	}
	if !track.suspect(rrClock(time.Now()), math.MinInt64) {
		t.Fatal("the slot is suspect as before")
	}
}

// 1. The client half-closes or we close after the wait, the node proved alive
// meanwhile: the key moves on. A long request that gets its first byte after the
// shift still works.
func TestRRSiteSilence_atTheEndOfTheWaitAndLateAnswer(t *testing.T) {
	for _, viaClose := range []bool{false, true} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		key := f.keyOn(t, "slow.example", 0)
		client, server := net.Pipe()
		defer server.Close()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		track := f.s.balancer.trackOf("a")
		start := time.Now()
		conn := newRRFirstByteConn(client, &rrConnWatch{s: f.s, member: "a", track: track, start: start, key: f.s.balancer.rrKeyOf(key, siteAddr("slow.example"))})
		conn.Write([]byte("GET /slow"))
		conn.wroteAt.Add(-int64(rrHangAfter + time.Second))
		track.markProved(rrClock(start.Add(time.Millisecond)))
		if viaClose {
			conn.Close()
		} else {
			conn.LxClientFinished(true)
		}
		if shift, _ := f.shift(key, "slow.example"); shift != 1 {
			t.Fatalf("viaClose=%v: the key moves on at the end of the wait, got %d", viaClose, shift)
		}
		if viaClose {
			continue
		}
		go server.Write([]byte("late"))
		buffer := make([]byte, 8)
		if n, err := conn.Read(buffer); n == 0 || err != nil {
			t.Fatalf("the long request still gets its answer: %d %v", n, err)
		}
		conn.Close()
	}
}

// 2a. A dial that ended in silence with no proof of the node keeps the slot under
// suspicion after it ended; a freeze does not lift it, a proof does.
func TestRRSilence_stickySuspicion(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["c"].set(&f.fb["c"].stall, "site.example", true)
	release := make(chan struct{})
	f.fb["c"].access.Lock()
	f.fb["c"].release = release
	f.fb["c"].access.Unlock()
	f.hold("c")
	f.s.balancer.heal.running.Store(true) // no check: only the suspicion is looked at
	key := f.keyOn(t, "site.example", 2)
	done := f.dialAsync(key, "site.example")
	track := f.s.balancer.trackOf("c")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	close(release) // the handshake times out
	if result := waitDial(t, done); result.err == nil {
		result.conn.Close()
	}
	f.s.balancer.heal.running.Store(false)
	heal := &f.s.balancer.heal
	if track.dials.oldest.Load() != 0 || !track.suspect(rrClock(time.Now()), heal.event()) {
		t.Fatal("after its silent dial ended, c stays suspect")
	}
	track.markProved(rrClock(time.Now()))
	if track.suspect(rrClock(time.Now()), heal.event()) {
		t.Fatal("a proof lifts it")
	}
	// A connection that started before an event of the network (a freeze) gives no
	// suspicion: the silence may have been the network's.
	track.markSilent(rrClock(time.Now()))
	if !track.suspect(rrClock(time.Now()), heal.event()) {
		t.Fatal("precondition: suspect again")
	}
	heal.freeze()
	if track.suspect(rrClock(time.Now()), heal.event()) {
		t.Fatal("the connection started before the freeze: no suspicion")
	}
}

// 2b. Reproduction of ps.log: a check from a reason ran during the stall and the
// freeze cut it short; after the freeze, the stalled node's slot gets its check at
// once, not after the gaps armed before.
func TestRRFreeze_firstCheckAfterTheFreezeAtOnce(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	heal := &f.s.balancer.heal
	now := time.Now()
	heal.lastHangEnd.Store(&now)
	heal.lastCheckEnd.Store(&now)
	heal.claimVerdict(f.s.balancer.trackOf("a"), rrClock(now))
	heal.releaseVerdict(f.s.balancer.trackOf("a"))
	heal.freeze()
	if heal.lastHangEnd.Load() != nil || heal.lastCheckEnd.Load() != nil || heal.verdictStarts != [rrVerdictBurst]int64{} {
		t.Fatal("the freeze drops the gaps armed before it")
	}
	// A check that a freeze cut short sets no gap.
	heal.running.Store(true)
	heal.endHangCheck()
	if heal.lastHangEnd.Load() != nil {
		t.Fatal("a check ended by a freeze sets no gap")
	}
	past := time.Now().Add(-time.Second)
	heal.frozenUntil.Store(&past) // the freeze is over
	// ... and it was some time ago: the stalled connection below started after it.
	heal.lastFreeze.Store(rrClock(time.Now().Add(-3 * rrHangAfter)))
	// c stays stopped: its dial hangs, its probe is silent.
	never := make(chan struct{})
	defer close(never)
	f.fb["c"].hold = never
	f.fb["c"].set(&f.fb["c"].stall, "site.example", true)
	key := f.keyOn(t, "site.example", 2)
	ctx, cancel := context.WithCancel(key)
	defer cancel()
	f.dialAsync(ctx, "site.example")
	track := f.s.balancer.trackOf("c")
	waitUntil(t, func() bool { return track.dials.oldest.Load() != 0 })
	ageTrack(track, rrHangAfter+time.Second)
	start := time.Now()
	if selected, _ := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example")); selected.Tag() == "c" {
		t.Fatal("the suspect slot is skipped")
	}
	waitUntil(t, func() bool { return f.g.history.LoadURLTestHistory("c") == nil })
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("c is struck within a probe of the first look, took %v", elapsed)
	}
}

// 3. A node that answered a probe while its connections wait is no part of a stall
// of the network: one stalled node and one proved one — no freeze. The i1
// reproduction (TestRRStall_networkBackFreezes) still freezes.
func TestRRStall_provedNodeIsNoStall(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	old := rrClock(time.Now().Add(-rrHangAfter - 2*time.Second))
	for _, tag := range []string{"a", "b"} {
		track := f.s.balancer.trackOf(tag)
		var entry rrTrackEntry
		track.track(&track.waits, &entry, old, nopKill{})
	}
	f.s.balancer.trackOf("a").markProved(old + int64(time.Second)) // a answered a probe while it waited
	f.s.balancer.heal.lastByte.Store(old)
	prover := f.s.balancer.trackOf("b") // b, stuck, gets its proof now (a slow probe answer)
	if f.s.rrNetworkBack(rrClock(time.Now()), prover, prover.proved.Load(), true) || f.s.balancer.heal.frozen() {
		t.Fatal("one stalled node and one that proved alive: no stall of the network")
	}
}

// lx:end SPEC 138
