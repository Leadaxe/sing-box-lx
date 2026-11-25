// lx:begin SPEC 138 round_robin review follow-ups (round 8)

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

// 1. Reproduction: a pool of one, its node dies; the first candidate outside the
// pool is dead too, the second is alive. The control is searched over every
// candidate: the second one answers, a is struck out and replaced; no freeze.
func TestRRBracket_poolOfOneFirstCandidateDead(t *testing.T) {
	shortProbes(t, 400*time.Millisecond)
	f := newShiftFixture(t, []string{"a", "c1", "c2"}, []string{"a"})
	never := make(chan struct{})
	defer close(never)
	f.fb["a"].hold, f.fb["c1"].hold = never, never
	f.s.rrStartCheck(f.fb["a"], false)
	f.waitCheck(t)
	if f.s.balancer.heal.frozen() {
		t.Fatal("a live candidate answered: no own network")
	}
	waitUntil(t, func() bool { return samePool(f.pool(), "c2") })
	if f.g.history.LoadURLTestHistory("a") != nil {
		t.Fatal("a is struck out")
	}
}

// 3. A UDP open that fails through a node leaves the slot to TCP: no suspicion.
func TestRRUDP_failedOpenNoTCPSuspicion(t *testing.T) {
	f := newUDPFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	f.udp["a"].listenFail.Store(true) // a SOCKS server with no UDP ASSOCIATE
	if conn, err := f.listen(context.Background()); err == nil {
		conn.Close()
	}
	f.waitCheck(t)
	if f.s.balancer.trackOf("a").silentSince.Load() != 0 {
		t.Fatal("a failed UDP open puts no TCP suspicion on the slot")
	}
}

// 4. A shift by silence confirms no failure, not even through the retry's byte.
func TestRRSilence_confirmCountsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	failure := f.s.rrNewFailure(rrPick{}, "a")
	failure.silence = true
	failure.setAlive()
	if failure.confirm() || failure.creditPending {
		t.Fatal("silence confirms nothing")
	}
}

// 5. A destination mark set by a proxy on the way to the leaf's server (a detour)
// is the node's path failing: no shift. The leaf's own mark shifts the key.
func TestRRProxyFailure_detourMarkIsTheNodes(t *testing.T) {
	for _, c := range []struct {
		tag   string
		shift int
	}{{"detour-socks", 0}, {"a", 1}} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		key := f.keyOn(t, "site.example", 0)
		err := &adapter.DestinationRefusedLx{Tag: c.tag, Err: E.New("socks5: request rejected, code=4")}
		conn, _ := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("site.example"), f.fb["a"], err, f.pickAt(key, "site.example", time.Now()))
		closeConn(conn)
		f.settleDeferred(t)
		if shift, _ := f.shift(key, "site.example"); shift != c.shift {
			t.Fatalf("mark by %s: shift %d, want %d", c.tag, shift, c.shift)
		}
	}
}

// 6. A node is credited once per key: one site closed only through it does not
// take it to quarantine; three keys do.
func TestRRShift_creditOncePerKey(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 20; i++ {
			f.s.balancer.rrCountConn(tag, &f.s.balancer.heal)
		}
	}
	one := rrKey{table: 1, valid: true}
	for i := 0; i < 5; i++ {
		f.s.rrCreditFailKey("a", one)
	}
	if f.fails("a") != 1 || f.s.balancer.isQuarantined("a") {
		t.Fatalf("one key counts once, got %d", f.fails("a"))
	}
	f.s.rrCreditFailKey("a", rrKey{table: 2, valid: true})
	f.s.rrCreditFailKey("a", rrKey{table: 3, valid: true})
	if f.fails("a") != 3 || !f.s.balancer.isQuarantined("a") {
		t.Fatalf("three keys: three failures and quarantine, got %d", f.fails("a"))
	}
}

// 7. The first byte of a connection that started before the last event of the
// network confirms no failure.
func TestRREvent_firstByteBeforeTheEventConfirmsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"})
	start := time.Now()
	failure := f.s.rrNewFailure(rrPick{start: start}, "a")
	failure.setAlive()
	f.s.balancer.heal.epochEnd.Store(rrClock(time.Now()))
	f.s.rrFirstByte(&rrConnWatch{s: f.s, member: "b", start: start, confirmFor: "a", failure: failure})
	if f.fails("a") != 0 {
		t.Fatal("a byte of a connection from before the event confirms nothing")
	}
}

// 9. A failed retry through a proxy puts the retry's slot under suspicion too.
func TestRRProxyFailure_retrySlotSuspect(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].siteDown.Store(true)
	f.fb["b"].siteDown.Store(true)
	f.s.balancer.heal.running.Store(true) // no check: the suspicion alone is under test
	defer f.s.balancer.heal.running.Store(false)
	f.hold("b")
	f.dial(t, f.keyOn(t, "site.example", 0), "site.example")
	if !f.s.balancer.trackOf("b").suspect(rrClock(time.Now()), f.s.balancer.heal.epoch()) {
		t.Fatal("the failed retry's slot is suspect")
	}
}

// 12. A sticky mark from before the last event of the network gets no look.
func TestRRTrack_attentionIgnoresStaleSilence(t *testing.T) {
	track := newRRTrack("a")
	silent := rrClock(time.Now())
	track.markSilent(silent)
	if !track.attention(rrClock(time.Now()), silent-1) {
		t.Fatal("precondition: a silence after the event is work for the pick")
	}
	if track.attention(rrClock(time.Now()), silent+1) {
		t.Fatal("a silence before the event is no work for the pick")
	}
}

// lx:end SPEC 138
