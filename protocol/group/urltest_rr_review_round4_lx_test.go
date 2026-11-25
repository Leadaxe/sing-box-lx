// lx:begin SPEC 138 round_robin review follow-ups: UDP waits, verdict budget, pick
// distribution under suspicion, retry target, verdict history, site-on-open marker

package group

import (
	"context"
	"math"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// suspectNow puts the slot of tag under suspicion: a TCP connection through it has
// waited for its first byte longer than rrHangAfter and the node proved nothing
// since. The look at the slot is held off, so only the pick sees the suspicion.
func suspectNow(t *testing.T, b *balancer, tag string) *rrTrackEntry {
	t.Helper()
	track := b.trackOf(tag)
	entry := &rrTrackEntry{}
	track.track(&track.waits, entry, rrClock(time.Now().Add(-rrHangAfter-time.Second)), nopKill{})
	track.hangLook.Store(rrClock(time.Now().Add(time.Hour)))
	if !track.suspect(rrClock(time.Now()), math.MinInt64) {
		t.Fatal("precondition: ", tag, " under suspicion")
	}
	return entry
}

// 1. A UDP flow without an answer is no suspicion and gets no verdict probe: only
// the reason for a check, with its 60 s gap.
func TestRRTrack_udpWaitNoSuspicion(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	track := f.s.balancer.trackOf("a")
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	flow := newRRFirstPacketConn(pc, &rrConnWatch{s: f.s, member: "a", track: track, packet: true, start: time.Now()})
	flow.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	ageTrack(track, rrHangAfter+time.Second)
	now := rrClock(time.Now())
	if track.suspect(now, math.MinInt64) {
		t.Fatal("a UDP flow without an answer is no suspicion")
	}
	gap := time.Now()
	f.s.balancer.heal.lastHangEnd.Store(&gap) // the gap of the checks from a reason
	f.s.rrOnPick(context.Background(), "a", track, now)
	time.Sleep(20 * time.Millisecond)
	if track.verdictLast.Load() != 0 || f.probes("a") != 0 {
		t.Fatal("a UDP flow without an answer gets no verdict probe")
	}
	f.s.balancer.heal.lastHangEnd.Store(nil)
	track.hangLook.Store(0)
	f.s.rrOnPick(context.Background(), "a", track, rrClock(time.Now()))
	f.waitCheck(t)
	if f.probes("a") != 1 || track.verdictLast.Load() != 0 {
		t.Fatalf("it is still a reason for a check, on the terms of rrHang: a probed %d, verdict %d", f.probes("a"), track.verdictLast.Load())
	}
}

// 2. Failures spread over every node of the pool: verdict probes start no more than
// rrVerdictBurst per window per group, one in flight.
func TestRRVerdict_groupBudget(t *testing.T) {
	tags := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	f := newShiftFixture(t, tags, tags)
	deadline := time.Now().Add(400 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		tag := tags[i%len(tags)]
		f.s.rrPend(f.fb[tag], f.s.rrNewFailure(rrPick{}, tag), false, rrVerdictNow)
		if inFlight := f.s.balancer.heal.verdicts.Load(); inFlight > 1 {
			t.Fatalf("%d verdict probes in flight", inFlight)
		}
		time.Sleep(2 * time.Millisecond)
	}
	waitUntil(t, func() bool { return f.s.balancer.heal.verdicts.Load() == 0 })
	var probes int32
	for _, tag := range tags {
		probes += f.probes(tag)
	}
	t.Logf("verdict probes started in 400 ms over %d nodes: %d", len(tags), probes)
	if probes > rrVerdictBurst {
		t.Fatalf("at most %d verdict probes per %v per group, got %d", rrVerdictBurst, rrVerdictWindow, probes)
	}
	// The failures left without a probe wait for the next pick of their node.
	deferred := 0
	for _, tag := range tags {
		if f.s.balancer.trackOf(tag).checkAfter.Load() != 0 {
			deferred++
		}
	}
	if deferred == 0 {
		t.Fatal("a failure without a probe is deferred to the next pick")
	}
}

// keyWithSlot returns a context whose sticky key takes slot of a pool of n slots.
func keyWithSlot(t *testing.T, b *balancer, slot int, n int, seed int) context.Context {
	t.Helper()
	for i := seed; i < seed+100000; i++ {
		metadata := &adapter.InboundContext{Domain: "site.example", ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/app/" + strconv.Itoa(i)}}
		ctx := adapter.WithContext(context.Background(), metadata)
		if b.rrKeyOf(ctx, siteAddr("site.example")).sticky%uint64(n) == uint64(slot) {
			return ctx
		}
	}
	t.Fatal("no key for slot ", slot)
	return nil
}

// 4. The keys of a dead slot keep their slots when an unrelated slot falls under
// suspicion; a key whose own target is suspect goes to the nearest slot after it
// (a detour), and a failure there shifts no key.
func TestRRSuspect_deadSlotKeysStay(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"})
	f.g.history.DeleteURLTestHistory("d")
	b := f.s.balancer
	targets := map[int]string{}
	contexts := map[int]context.Context{}
	for i := 0; i < 40; i++ {
		ctx := keyWithSlot(t, b, 3, 4, i*1000)
		selected, _ := f.s.selectBalanced(ctx, N.NetworkTCP, siteAddr("site.example"))
		targets[i], contexts[i] = selected.Tag(), ctx
	}
	suspectNow(t, b, "a")
	moved := 0
	for i, ctx := range contexts {
		selected, picked := f.s.selectBalanced(ctx, N.NetworkTCP, siteAddr("site.example"))
		switch {
		case targets[i] == "a":
			if selected.Tag() != "b" || !picked.detour {
				t.Fatalf("a key whose target is suspect goes to the next slot after it, got %s", selected.Tag())
			}
		case selected.Tag() != targets[i]:
			moved++
		}
	}
	if moved != 0 {
		t.Fatalf("%d keys of the dead slot moved because an unrelated slot fell under suspicion", moved)
	}
}

func TestRRSuspect_detourFailureShiftsNothing(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["b"].set(&f.fb["b"].openFail, "site.example", true)
	suspectNow(t, f.s.balancer, "a")
	key := f.keyOn(t, "site.example", 0)
	selected, picked := f.s.selectBalanced(key, N.NetworkTCP, siteAddr("site.example"))
	if selected.Tag() != "b" || !picked.detour {
		t.Fatal("precondition: a detour to b")
	}
	conn, _ := f.s.rrHealDial(key, N.NetworkTCP, siteAddr("site.example"), f.fb["b"], E.New("dial failed"), picked)
	closeConn(conn)
	f.waitCheck(t)
	if f.probes("b") == 0 {
		t.Fatal("precondition: b checked and alive")
	}
	if shift, _ := f.shift(key, "site.example"); shift != 0 {
		t.Fatalf("a failure on a detour slot shifts no key, got %d", shift)
	}
}

// 5a. A suspect slot the pick went past gets its look even when it is no key's own
// slot: here the keys of a dead slot land on it.
func TestRRSuspect_skippedSlotLooked(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"})
	f.g.history.DeleteURLTestHistory("d")
	b := f.s.balancer
	var ctx context.Context
	var target string
	for i := 0; ; i++ {
		ctx = keyWithSlot(t, b, 3, 4, i*1000)
		selected, _ := f.s.selectBalanced(ctx, N.NetworkTCP, siteAddr("site.example"))
		if target = selected.Tag(); target != "d" {
			break
		}
	}
	suspectNow(t, b, target)
	b.trackOf(target).hangLook.Store(0)
	f.s.selectBalanced(ctx, N.NetworkTCP, siteAddr("site.example"))
	f.waitCheck(t)
	if f.probes(target) == 0 {
		t.Fatalf("the suspect slot %s the pick went past is looked at", target)
	}
}

// 5b. The retry skips a slot under suspicion while another one is fine, as the pick
// of the shifted key will.
func TestRRSuspect_retrySkipsSuspectSlot(t *testing.T) {
	f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
	f.fb["a"].set(&f.fb["a"].openFail, "site.example", true)
	suspectNow(t, f.s.balancer, "b")
	key := f.keyOn(t, "site.example", 0)
	if _, answered := f.dial(t, key, "site.example"); !answered {
		t.Fatal("the retry answers")
	}
	if f.dials("b") != 0 || f.dials("c") != 1 {
		t.Fatalf("the retry goes past suspect b to c: b %d c %d", f.dials("b"), f.dials("c"))
	}
}

// 6. A verdict probe of a node found dead (or replaced) while it ran writes no
// history: the replaced node would look like a live candidate again.
func TestRRVerdict_noHistoryAfterDeadVerdict(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		f := newShiftFixture(t, []string{"a", "b", "c", "d"}, []string{"a", "b", "c"})
		track := f.s.balancer.trackOf("a")
		hold := make(chan struct{})
		f.fb["a"].hold = hold
		f.g.history.DeleteURLTestHistory("a")
		if !f.s.rrVerdictProbe(f.fb["a"], track, nil) {
			t.Fatal("precondition: the verdict probe starts")
		}
		waitUntil(t, func() bool { return f.probes("a") == 1 })
		if replaced {
			f.s.balancer.replaceSlot("a", "d")
		} else {
			f.s.rrDead(track)
		}
		close(hold)
		waitUntil(t, func() bool { return f.s.balancer.heal.verdicts.Load() == 0 })
		if f.g.history.LoadURLTestHistory("a") != nil {
			t.Fatalf("replaced=%v: the answer of a node found dead meanwhile is not written", replaced)
		}
	}
}

// destinationNode is an outbound that dials the destination itself (as MASQUE).
type destinationNode struct {
	adapter.Outbound
	tag string
}

func (n *destinationNode) Type() string             { return C.TypeMASQUE }
func (n *destinationNode) Tag() string              { return n.tag }
func (n *destinationNode) DialsDestinationLx() bool { return true }
func (n *destinationNode) Network() []string        { return []string{N.NetworkTCP} }
func (n *destinationNode) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, syscall.ECONNREFUSED
}

// 7. An outbound that dials the destination itself: its refused open is the site's
// answer for SPEC 054 (no dead path) and for the round_robin self-heal (the site is
// under suspicion).
func TestSiteOnOpen_destinationDialer(t *testing.T) {
	node := &destinationNode{tag: "m"}
	manager := &fakeManager{byTag: map[string]adapter.Outbound{"m": node}}
	if !siteOnOpenLx(context.Background(), manager, node) {
		t.Fatal("an outbound that dials the destination itself goes to the site on open")
	}
	g := &URLTestGroup{ctx: context.Background(), outbound: manager}
	if g.pathDead(node, syscall.ECONNREFUSED) {
		t.Fatal("SPEC 054: its refused open is no dead path")
	}
	proxy := &cycleNode{tag: "p"}
	manager.byTag["p"] = proxy
	if siteOnOpenLx(context.Background(), manager, proxy) || !g.pathDead(proxy, syscall.ECONNREFUSED) {
		t.Fatal("a proxy's refused open is a dead path")
	}
}

// lx:end SPEC 138
