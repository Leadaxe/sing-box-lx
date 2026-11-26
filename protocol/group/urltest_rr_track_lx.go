package group

// lx:begin SPEC 138 round_robin: what a slot knows about its node
//
// The rules named here are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/
// ALGORITHM.md. Every slot of a round_robin pool carries a tracker of its node
// (rrTrack). It lives as long as the node holds the slot: setSlots and replaceSlot
// hand it over to the same node and give a newcomer a fresh one.
//
// A tracker holds:
//   - the unfinished exchanges through the node: TCP dials in flight, TCP
//     connections waiting for the first byte since the client sent (the wait starts
//     at w), UDP opens and UDP flows waiting for an answer. Each list is ordered by
//     its time, so its oldest entry is one atomic load;
//   - the proofs of the path through the node, (a', b') — the start and the moment
//     of an ANSWER (A1): a first byte, a probe answer, an answer of the scheduled
//     run. Only the front (later start, earlier answer) is kept; it answers Live
//     (the rule of the half) and the premise "an ANSWER via the node between the
//     attempts" of K2;
//   - the latest start of an ANSWER to each key via the node, in cells by the hash
//     of the key: the premise "no ANSWER to the key that started at or after a1" of
//     K2. Another key in the same cell only makes K2 miss a shift (I7);
//   - the suspicion of the slot (S1–S3): the pick goes round it while another slot
//     is fine. It is no verdict and changes no history, key or counter;
//   - the Q1 signs waiting for a proof via the node after them.
//
// No timers: the age of an exchange is read when a pick looks at its slot (at most
// once per rrLook) and when a probe of the node answers.

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// rrHangAfter is H: an unfinished exchange this old is a slow sign (S2).
	rrHangAfter = 5 * time.Second
	// rrRTO is ρ, the initial retransmission timeout (RFC 6298) of the rule of the half.
	rrRTO = time.Second
	// rrLook — a pick looks at the exchanges of a slot no more often than this.
	rrLook = time.Second
	// rrClockJump — a gap between the wall and the monotonic clock beyond this is an
	// event (N3).
	rrClockJump = 10 * time.Second
	// The limits of the life probe: one in flight per node, one per node every
	// rrLifeGap, rrLifeBurst per group in rrLifeWindow; one by a UDP wait per group
	// every rrUDPLifeGap on top.
	rrLifeGap    = 3 * time.Second
	rrLifeBurst  = 4
	rrLifeWindow = 15 * time.Second
	rrUDPLifeGap = 60 * time.Second
	// rrPendingMax bounds the Q1 signs waiting on one node; the oldest goes first.
	rrPendingMax = 64
	// rrProofMax bounds the proofs kept per node. An append keeps every proof at
	// least rrProofGrain after the one before the previous, so a front built by
	// appends spans at least (rrProofMax/2 - 1) * rrProofGrain = 31 s >= 2T. A proof
	// inserted inside the front (an earlier start and an earlier answer than the
	// kept ones) can make it shorter; a premise whose proof dropped out then gives
	// no inference (conservative).
	rrProofMax   = 64
	rrProofGrain = time.Second
	// rrKeyCells is the number of cells of the latest ANSWER to a key per node (K2).
	rrKeyCells = 256
)

// rrHorizon is 2T: the life of a suspicion and the time a Q1 sign waits for its
// proof.
func rrHorizon() int64 {
	return 2 * int64(rrProbeTimeout())
}

// errRRStruck is the cause of a dial in flight cancelled by D1 on its node.
var errRRStruck = E.New("round_robin: the node is struck out as dead")

// rrUnwatched reports an exchange that takes no part in the self-heal: a
// connection test (SPEC 139) and a delay measurement through the group.
func rrUnwatched(ctx context.Context) bool {
	return lxtrace.IsProbe(ctx) || urltest.IsMeasureLx(ctx)
}

// rrDialMark is the context key of the mark of a round_robin open (§5).
type rrDialMark struct{}

// rrMarkDial marks ctx as the open of a round_robin group.
func rrMarkDial(ctx context.Context) context.Context {
	if rrInnerDial(ctx) {
		return ctx
	}
	return context.WithValue(ctx, rrDialMark{}, true)
}

// rrInnerDial reports an open that an outer round_robin group makes: the retry is
// that group's.
func rrInnerDial(ctx context.Context) bool {
	marked, _ := ctx.Value(rrDialMark{}).(bool)
	return marked
}

// rrTime is the time of an rrClock mark (with the monotonic reading).
func rrTime(clock int64) time.Time {
	return rrClockBase.Add(time.Duration(clock - 1))
}

// rrHalf is the bound of the rule of the half (§3): a proof (a', b') with a' >= w
// and b' <= rrHalf(w, t) shows the request sent at w delivered by t.
func rrHalf(w, t int64) int64 {
	return (t + w - int64(rrRTO)) / 2
}

// --- unfinished exchanges ---------------------------------------------------------

// rrKillable is an unfinished exchange that D1 ends.
type rrKillable interface {
	rrKill()
}

// rrTrackEntry is one unfinished exchange in a list of a tracker; it is embedded in
// its owner, so tracking allocates nothing.
type rrTrackEntry struct {
	prev, next *rrTrackEntry
	list       *rrTrackList
	since      int64 // rrClock
	owner      rrKillable
}

// rrTrackList is a list of unfinished exchanges ordered by since; the caller holds
// the tracker's access.
type rrTrackList struct {
	head, tail *rrTrackEntry
	oldest     atomic.Int64 // since of head; 0 for an empty list
}

func (l *rrTrackList) push(e *rrTrackEntry) {
	e.list, e.prev, e.next = l, l.tail, nil
	if l.tail != nil {
		l.tail.next = e
	} else {
		l.head = e
		l.oldest.Store(e.since)
	}
	l.tail = e
}

func (l *rrTrackList) remove(e *rrTrackEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		l.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		l.tail = e.prev
	}
	e.list, e.prev, e.next = nil, nil, nil
	if l.head != nil {
		l.oldest.Store(l.head.since)
	} else {
		l.oldest.Store(0)
	}
}

// newestBefore returns the since of the newest entry not newer than cutoff.
func (l *rrTrackList) newestBefore(cutoff int64) (newest int64, found bool) {
	for e := l.head; e != nil && e.since <= cutoff; e = e.next {
		newest, found = e.since, true
	}
	return
}

// drain unlinks every entry and appends its owner to owners.
func (l *rrTrackList) drain(owners []rrKillable) []rrKillable {
	for e := l.head; e != nil; {
		next := e.next
		owners = append(owners, e.owner)
		e.list, e.prev, e.next = nil, nil, nil
		e = next
	}
	l.head, l.tail = nil, nil
	l.oldest.Store(0)
	return owners
}

// rrProof is one proof of the path through a node: an ANSWER of an exchange that
// started at a and answered at b (rrClock).
type rrProof struct {
	a, b int64
}

// rrTrack is the tracker of one pool member (see the file header).
type rrTrack struct {
	tag    string
	access sync.Mutex  // the lists and pending
	dials  rrTrackList // TCP dials in flight
	waits  rrTrackList // TCP connections: the client sent, no first byte yet (since = w)
	pwaits rrTrackList // UDP opens in flight and UDP flows sent with no answer
	// pending are the Q1 signs waiting for a proof via the node after them, in the
	// order of their outcome.
	pending  []*rrSign
	pendingN atomic.Int32
	lookAt   atomic.Int64 // rrClock before which a pick does not look at the slot
	// proved is the latest start a' of a proof of the node; math.MinInt64 for none.
	proved      atomic.Int64
	proofAccess sync.Mutex
	proofs      [rrProofMax]rrProof // the front: a' and b' both ascending
	proofN      int
	// keyAnswers are the cells of the latest start of an ANSWER to a key via the
	// node, by the table hash of the key (K2); 0 for none.
	keyAnswers [rrKeyCells]atomic.Int64
	// The suspicion (S1–S3): from the start a of the sign that set it, until the
	// rrClock mark; a proof with a' >= a lifts it.
	suspectFrom  atomic.Int64
	suspectUntil atomic.Int64
	// wantCheck: a life probe of the node had no ANSWER, and its suspicion asks for
	// the check until a verdict comes (D4, D5).
	wantCheck atomic.Bool
	lifeBusy  atomic.Bool  // a life probe of the node is in flight
	lifeLast  atomic.Int64 // rrClock of the start of its last life probe
	deadAt    atomic.Int64 // rrClock of the last D1 on the node
}

func newRRTrack(tag string) *rrTrack {
	t := &rrTrack{tag: tag}
	t.proved.Store(math.MinInt64)
	return t
}

// track registers an unfinished exchange in list.
func (t *rrTrack) track(list *rrTrackList, e *rrTrackEntry, since int64, owner rrKillable) {
	t.access.Lock()
	defer t.access.Unlock()
	if e.list != nil {
		return
	}
	e.since, e.owner = since, owner
	if tail := list.tail; tail != nil && tail.since > since {
		e.since = tail.since // a concurrent register with a later clock got the lock first
	}
	list.push(e)
}

// untrack removes an unfinished exchange; a no-op for one not registered.
func (t *rrTrack) untrack(e *rrTrackEntry) {
	t.access.Lock()
	defer t.access.Unlock()
	if e.list != nil {
		e.list.remove(e)
	}
}

// markProved moves proved to at.
func (t *rrTrack) markProved(at int64) {
	for {
		old := t.proved.Load()
		if old >= at || t.proved.CompareAndSwap(old, at) {
			return
		}
	}
}

// addProof records the proof (a, b). The front keeps both a' and b' ascending: a
// proof with an earlier start and a later answer than another proves less. Two
// answers closer than rrProofGrain keep the later one only, which proves less for a
// start before it (conservative).
func (t *rrTrack) addProof(a, b int64) {
	t.markProved(a)
	t.proofAccess.Lock()
	defer t.proofAccess.Unlock()
	n := t.proofN
	i := sort.Search(n, func(i int) bool { return t.proofs[i].a >= a })
	if i < n && t.proofs[i].b <= b {
		return
	}
	j := i
	for j > 0 && t.proofs[j-1].b >= b {
		j--
	}
	if j == i && i == n && j >= 2 && b-t.proofs[j-2].b < int64(rrProofGrain) {
		j-- // too close to the one before the last: it replaces the last
	}
	if j == i && n == rrProofMax {
		if i == 0 {
			return // older than every kept proof: the front is full, it goes (conservative)
		}
		copy(t.proofs[:], t.proofs[1:n])
		n--
		i--
		j--
	}
	copy(t.proofs[j+1:], t.proofs[i:n])
	t.proofs[j] = rrProof{a, b}
	t.proofN = n - (i - j) + 1
}

// keyAnswered records an ANSWER to key (the table hash) via the node that started
// at a (K2). It is written before the proof of the same ANSWER (rrProof), so a
// reader that saw the proof sees the key too.
func (t *rrTrack) keyAnswered(key uint64, a int64) {
	cell := &t.keyAnswers[key%rrKeyCells]
	for {
		old := cell.Load()
		if old >= a || cell.CompareAndSwap(old, a) {
			return
		}
	}
}

// keyAnsweredSince reports whether key may have had an ANSWER via the node that
// started at or after from: its cell holds such a start (of the key or of another
// key in the same cell).
func (t *rrTrack) keyAnsweredSince(key uint64, from int64) bool {
	return t.keyAnswers[key%rrKeyCells].Load() >= from
}

// live reports whether a proof (a', b') with a' >= from and b' <= to is known.
func (t *rrTrack) live(from, to int64) bool {
	if t.proved.Load() < from {
		return false
	}
	t.proofAccess.Lock()
	defer t.proofAccess.Unlock()
	i := sort.Search(t.proofN, func(i int) bool { return t.proofs[i].a >= from })
	return i < t.proofN && t.proofs[i].b <= to
}

// suspectAt sets or extends the suspicion from a sign that started at from (S1
// after a failed life probe, S2): until 2T after now. Over a suspicion that still
// holds the later start wins: a proof lifts it only when it covers every sign.
func (t *rrTrack) suspectAt(from, now int64) {
	if old := t.suspectFrom.Load(); t.suspected(now, math.MinInt64) && old > from {
		from = old
	}
	t.suspectFrom.Store(from)
	until := now + rrHorizon()
	for {
		old := t.suspectUntil.Load()
		if old >= until || t.suspectUntil.CompareAndSwap(old, until) {
			return
		}
	}
}

// clearSuspicion lifts the suspicion (S3: D1, D3, the scheduled run).
func (t *rrTrack) clearSuspicion() {
	t.suspectUntil.Store(0)
	t.wantCheck.Store(false)
}

// suspected reports the suspicion set by a sign; a proof with a' >= a, E and the
// term lift it (S3).
func (t *rrTrack) suspected(now, epoch int64) bool {
	from := t.suspectFrom.Load()
	return now < t.suspectUntil.Load() && from >= epoch && t.proved.Load() < from
}

// suspect reports, with a few atomic loads and no lock, whether the pick goes round
// the slot: a suspicion, or a TCP dial or wait older than H with no proof since it
// started (S2 at once).
func (t *rrTrack) suspect(now, epoch int64) bool {
	if t.suspected(now, epoch) {
		return true
	}
	oldest := t.dials.oldest.Load()
	if w := t.waits.oldest.Load(); w != 0 && (oldest == 0 || w < oldest) {
		oldest = w
	}
	return oldest != 0 && oldest <= now-int64(rrHangAfter) && oldest >= epoch && t.proved.Load() < oldest
}

// attention reports, with a few atomic loads, whether a pick at now has work for
// the slot: an exchange older than H, or a suspicion asking for the check.
func (t *rrTrack) attention(now int64) bool {
	if now < t.lookAt.Load() {
		return false
	}
	if t.wantCheck.Load() {
		return true
	}
	cutoff := now - int64(rrHangAfter)
	for _, list := range [3]*rrTrackList{&t.dials, &t.waits, &t.pwaits} {
		if oldest := list.oldest.Load(); oldest != 0 && oldest <= cutoff {
			return true
		}
	}
	return false
}

// newestHung returns the since of the newest TCP dial in flight or wait not newer
// than cutoff.
func (t *rrTrack) newestHung(cutoff int64) (int64, bool) {
	t.access.Lock()
	defer t.access.Unlock()
	newest, found := t.dials.newestBefore(cutoff)
	if w, waiting := t.waits.newestBefore(cutoff); waiting && (!found || w > newest) {
		newest, found = w, true
	}
	return newest, found
}

// newestPacketWait returns the since of the newest UDP exchange waiting not newer
// than cutoff.
func (t *rrTrack) newestPacketWait(cutoff int64) (int64, bool) {
	t.access.Lock()
	defer t.access.Unlock()
	return t.pwaits.newestBefore(cutoff)
}

// kill ends every unfinished exchange of the node (D1). A dial is marked struck
// under access, so a dial that fails on its own meanwhile still knows the verdict.
func (t *rrTrack) kill() int {
	t.access.Lock()
	owners := t.dials.drain(nil)
	owners = t.pwaits.drain(owners)
	for _, owner := range owners {
		if dial, isDial := owner.(*rrDial); isDial {
			dial.struck.Store(true)
		}
	}
	owners = t.waits.drain(owners)
	t.access.Unlock()
	for _, owner := range owners {
		owner.rrKill()
	}
	return len(owners)
}

// addPending records a Q1 sign waiting for a proof via the node.
func (t *rrTrack) addPending(sign *rrSign) {
	t.access.Lock()
	defer t.access.Unlock()
	if len(t.pending) >= rrPendingMax {
		copy(t.pending, t.pending[1:])
		t.pending[len(t.pending)-1] = nil
		t.pending = t.pending[:len(t.pending)-1]
	}
	t.pending = append(t.pending, sign)
	t.pendingN.Store(int32(len(t.pending)))
}

// takeProved removes the signs a proof that started at a settles (Q1: a' >= b,
// received within 2T of b at now) and drops those begun before epoch or too old.
func (t *rrTrack) takeProved(a, epoch, now int64) []*rrSign {
	t.access.Lock()
	defer t.access.Unlock()
	var settled []*rrSign
	kept := t.pending[:0]
	for _, sign := range t.pending {
		switch {
		case sign.b < epoch || now-sign.b > rrHorizon():
		case sign.b <= a:
			settled = append(settled, sign)
		default:
			kept = append(kept, sign)
		}
	}
	for i := len(kept); i < len(t.pending); i++ {
		t.pending[i] = nil
	}
	t.pending = kept
	t.pendingN.Store(int32(len(kept)))
	return settled
}

// dropPending drops every Q1 sign of the node (D1: the signs of a dead node do not
// count; the node left the slot).
func (t *rrTrack) dropPending() {
	t.access.Lock()
	defer t.access.Unlock()
	t.pending = nil
	t.pendingN.Store(0)
}

// --- slots ------------------------------------------------------------------------

// trackOf returns the tracker of the pool member tag; nil when it holds no slot.
func (b *balancer) trackOf(tag string) *rrTrack {
	for _, entry := range b.slotsSnapshot() {
		if entry.tag == tag {
			return entry.track
		}
	}
	return nil
}

// slotTrack returns the tracker of tag from old, or a fresh one; nil for an empty slot.
func slotTrack(old []slot, tag string) *rrTrack {
	if tag == "" {
		return nil
	}
	for _, entry := range old {
		if entry.tag == tag && entry.track != nil {
			return entry.track
		}
	}
	return newRRTrack(tag)
}

// departedTracks returns the trackers of old whose node holds no slot in next.
func departedTracks(old, next []slot) []*rrTrack {
	var departed []*rrTrack
	for _, entry := range old {
		if entry.track != nil && slotIndex(next, entry.tag) < 0 {
			departed = append(departed, entry.track)
		}
	}
	return departed
}

// --- dials in flight --------------------------------------------------------------

// rrDial is a dial of a pool member in flight: its own cancellable context and its
// entry in the tracker. After the dial it lives with the connection: the context is
// released when the connection closes (a node may tie the connection to the dial
// context, e.g. a gRPC or HTTP/2 stream).
type rrDial struct {
	entry  rrTrackEntry
	track  *rrTrack
	ctx    context.Context
	cancel context.CancelCauseFunc
	struck atomic.Bool // D1 took it off the list (set under track.access)
}

func (d *rrDial) rrKill() {
	d.cancel(errRRStruck)
}

// opened ends the tracking of a dial that returned a connection.
func (d *rrDial) opened() {
	if d != nil {
		d.track.untrack(&d.entry)
	}
}

// failed ends the tracking of a dial that returned an error and releases its
// context; it reports whether D1 on the node cancelled it.
func (d *rrDial) failed() bool {
	if d == nil {
		return false
	}
	d.track.untrack(&d.entry)
	struck := d.struck.Load()
	d.cancel(nil)
	return struck
}

// release releases the context of the dial: its connection is closed.
func (d *rrDial) release() {
	if d != nil {
		d.cancel(nil)
	}
}

// rrTrackDial starts tracking a dial of the pool member member through its chain
// leaf leaf; it returns the context to dial on. Not tracked (ctx as is, nil): a
// connection test, a node out of the pool, a nested urltest (it watches its own
// connections) and a direct leaf (its connection is not wrapped, nothing would
// release the context). A UDP open waits in pwaits: a hung UDP open makes no TCP
// suspicion (V9). A tracked dial is always wrapped on success (rrWatch).
func (s *URLTest) rrTrackDial(ctx context.Context, track *rrTrack, member adapter.Outbound, leaf adapter.Outbound, since int64, packet bool) (context.Context, *rrDial) {
	if track == nil || rrUnwatched(ctx) || rrNestedURLTest(member) || rrDirectLeaf(leaf) {
		return ctx, nil
	}
	dialCtx, cancel := context.WithCancelCause(ctx)
	dial := &rrDial{track: track, ctx: dialCtx, cancel: cancel}
	list := &track.dials
	if packet {
		list = &track.pwaits
	}
	track.track(list, &dial.entry, since, dial)
	return dialCtx, dial
}

// rrDialStart tracks the dial of the pick (DialContext, ListenPacket) through its
// chain leaf; it returns the context to dial on and records the dial in picked.
// A no-op outside round_robin and for a pick outside the pool.
// The context of the dial carries the mark of a round_robin open (rrMarkDial): a
// round_robin group on the way (a detour, a member of another group) finds it and
// leaves the retry to the outer one (§5); picked.inner records the mark found.
// A member that is a nested urltest gets no mark: this group does not retry it
// (rrHealRetry), the nested one does — exactly one group of the chain retries.
func (s *URLTest) rrDialStart(ctx context.Context, leaf adapter.Outbound, picked *rrPick) context.Context {
	if s.balancer == nil {
		return ctx
	}
	picked.inner = rrInnerDial(ctx)
	if !rrNestedURLTest(leaf) {
		ctx = rrMarkDial(ctx)
	}
	if picked.track == nil || picked.track.tag != leaf.Tag() {
		return ctx
	}
	member, loaded := s.outbound.Outbound(leaf.Tag())
	if !loaded {
		return ctx
	}
	ctx, picked.dial = s.rrTrackDial(ctx, picked.track, member, leaf, rrClock(picked.start), picked.packet)
	return ctx
}

// --- the pick --------------------------------------------------------------------

// rrWire connects the round_robin pick to the group.
func (s *URLTest) rrWire() {
	s.balancer.rr = s
}

// rrOnPick is the look at the slot of node tag by a pick (at most once per rrLook):
// SILENT (§2), the slow sign of a TCP exchange older than H (S2: the life probe and
// the suspicion at once), a UDP wait (the life probe alone, V9), and a suspicion
// asking for the check (D4, D5).
func (s *URLTest) rrOnPick(ctx context.Context, tag string, t *rrTrack, now int64) {
	if rrUnwatched(ctx) || s.group.ctx.Err() != nil {
		return
	}
	look := t.lookAt.Load()
	if now < look || !t.lookAt.CompareAndSwap(look, now+int64(rrLook)) {
		return
	}
	heal := &s.balancer.heal
	node, loaded := s.outbound.Outbound(tag)
	if !loaded || heal.frozen() {
		return
	}
	epoch := heal.epoch()
	s.rrInspect(t, now)
	cutoff := now - int64(rrHangAfter)
	if since, found := t.newestHung(cutoff); found && since >= epoch && t.proved.Load() < since {
		t.suspectAt(since, now) // S2: the bypass at once, also when the limits hold the probe back (V10)
		s.rrLifeProbe(node, t, since, false)
	} else if packet, waiting := t.newestPacketWait(cutoff); waiting && packet >= epoch && t.proved.Load() < packet {
		s.rrLifeProbe(node, t, packet, true)
	}
	if t.wantCheck.Load() && t.suspected(now, epoch) {
		s.rrStartRound(s.rrHealCheck) // D4, D5: the suspicion stays and starts the next check
	}
}

// rrPoolLook is the look at the whole pool by a pick (at most once a second): an
// empty slot (D1 found no replacement) or the slot of a quarantined node starts the
// replacement search when the pause is over (§4.2, W1) — the search only, never the
// check of the pool (I9). The pick of a probe or a measurement starts nothing.
func (s *URLTest) rrPoolLook(ctx context.Context) {
	if rrUnwatched(ctx) || !s.balancer.heal.ready(time.Now()) || s.group.checking.Load() || s.group.ctx.Err() != nil {
		return
	}
	if s.rrSlotToVerify() {
		s.rrStartRound(s.rrHealCheck) // lx: SPEC 019 — the unverified pool nodes are verified first, with a control
	} else if s.rrSlotToFill() {
		s.rrStartSearch()
	}
}

// rrLifeProbe probes node alone, outside the check and its gap (§4.2): at most one
// in flight per node, one per node every rrLifeGap, rrLifeBurst per group in
// rrLifeWindow, and by a UDP wait (udp) one per group every rrUDPLifeGap on top.
// ANSWER is D3. Without it the suspicion from the sign that started at from is set
// — not for a UDP wait (V9) — and the check starts. It reports whether the probe
// started; a probe the limits held back starts nothing (V10).
//
// W4: while the step of the pause is above the base and the pause goes on, no life
// probe starts. The probe takes its place in the queue of the core (§4.7); its
// start is the moment it flies.
func (s *URLTest) rrLifeProbe(node adapter.Outbound, t *rrTrack, from int64, udp bool) bool {
	heal := &s.balancer.heal
	now := time.Now()
	if t == nil || heal.frozen() || s.group.ctx.Err() != nil || heal.lifeHeld(now) ||
		!heal.claimLife(t, rrClock(now), udp) {
		return false
	}
	go func() {
		defer t.lifeBusy.Store(false)
		ctx, cancel := context.WithCancel(s.group.ctx)
		defer cancel()
		queue := s.group.rrQueue()
		if !queue.acquire(ctx, &heal.queue, 1) {
			return
		}
		probe := s.group.rrSelfProbe(ctx, node, time.Time{}, false)
		queue.release(1)
		// N0, and a node found dead or gone while it was probed: the outcome is no
		// longer about this slot.
		if ctx.Err() != nil || probe.leaf == "" || probe.cancelled() || !heal.counts(probe.started) ||
			s.balancer.trackOf(probe.member) != t || t.deadAt.Load() >= rrClock(probe.started) {
			return
		}
		if probe.answered {
			s.rrAlive(probe)
			return
		}
		if !udp {
			t.suspectAt(from, rrClock(probe.ended)) // S1: the bypass after the failed life probe
		}
		t.wantCheck.Store(true)
		s.rrStartRound(s.rrHealCheck)
	}()
	return true
}

// rrAlive is D3 on a probe that answered: its history is written; for a pool
// member the suspicion lifts and the proof is recorded (rrProof).
func (s *URLTest) rrAlive(probe rrProbe) {
	s.group.rrStore(probe)
	s.balancer.heal.probeAnswered() // W3
	t := s.balancer.trackOf(probe.member)
	if t == nil {
		return
	}
	t.clearSuspicion()
	s.rrProof(t, rrClock(probe.started), rrClock(probe.ended))
	s.rrInspect(t, rrClock(probe.ended)) // SILENT is read at the answer of a probe (§2)
}

// rrProof is an ANSWER via the node of t that started at a and came at b (A1): the
// proof is kept and the Q1 signs it settles count. An exchange begun before E
// proves nothing (I4).
func (s *URLTest) rrProof(t *rrTrack, a, b int64) {
	epoch := s.balancer.heal.epoch()
	if a < epoch {
		return
	}
	t.addProof(a, b)
	if t.pendingN.Load() > 0 {
		for _, sign := range t.takeProved(a, epoch, b) {
			sign.add(rrSignProof)
		}
	}
}

// rrRunAnswer is a probe of the scheduled run that answered for tag: a proof via
// the pool member of that tag. The start of the probe is not known; T before the
// answer bounds it from below.
func (s *URLTest) rrRunAnswer(tag string) {
	if s.balancer.heal.frozen() {
		return
	}
	s.balancer.heal.probeAnswered() // W3: an ANSWER of the scheduled or the manual run
	if t := s.balancer.trackOf(tag); t != nil {
		now := rrClock(time.Now())
		s.rrProof(t, now-int64(rrProbeTimeout()), now)
		s.rrInspect(t, now)
	}
}

// rrClockLook compares the wall and the monotonic clock (N3), at most once a
// second: on the pick and at every outcome, before the rules. A jump beyond
// rrClockJump either way is a sleep the monotonic clock missed (darwin, linux,
// android with no wake signal) or a clock change: an event. Every look resets the
// base of the comparison.
func (b *balancer) rrClockLook(at time.Time) {
	now := rrClock(at)
	if b.rr == nil || now < b.clockLook.Load() || !b.clockAccess.TryLock() {
		return
	}
	// The pair of readings changes as one, under the lock: a look delayed between
	// the two would compare a new monotonic reading with an old wall one.
	var drift time.Duration
	if b.clockMono != 0 {
		drift = time.Duration((at.UnixNano() - b.clockWall) - (now - b.clockMono))
	}
	b.clockMono, b.clockWall = now, at.UnixNano()
	b.clockLook.Store(now + int64(time.Second))
	b.clockAccess.Unlock()
	if drift > rrClockJump || drift < -rrClockJump {
		b.rr.rrClockJump(drift)
	}
}

// rrClockJump is the N3 event of a clock jump (rrClockLook).
func (s *URLTest) rrClockJump(drift time.Duration) {
	s.balancer.heal.freeze()
	s.logger.Info("round_robin: the wall clock moved ", drift.Round(time.Second), " apart from the monotonic one (a sleep with no wake signal, or a clock change); self-heal frozen for ", rrHealFreeze)
}

// rrInspect is the look at the TCP waits of the node of t at now for SILENT (§2):
// a wait older than T_site (= T) whose path through the node is confirmed in the
// first half of it (Live). Each such connection gets SILENT once (rrSilent); it is
// not closed — an answer may still come and cancel it.
func (s *URLTest) rrInspect(t *rrTrack, now int64) {
	cutoff := now - int64(rrProbeTimeout())
	if oldest := t.waits.oldest.Load(); oldest == 0 || oldest > cutoff {
		return
	}
	epoch := s.balancer.heal.epoch()
	var silent []*rrFirstByteConn
	t.access.Lock()
	for e := t.waits.head; e != nil && e.since <= cutoff; e = e.next {
		c, isConn := e.owner.(*rrFirstByteConn)
		if isConn && c.state.Load()&(rrStateSilent|rrStateEnded|rrStateHalfClosed) == 0 && rrClock(c.watch.start) >= epoch {
			silent = append(silent, c)
		}
	}
	t.access.Unlock()
	for _, c := range silent {
		// The first byte may have come since the list was read: SILENT is set only
		// while the connection has no outcome, in one step (§2).
		if w := c.wroteAt.Load(); t.live(w, rrHalf(w, now)) && c.setUnless(rrStateSilent, rrStateEnded|rrStateHalfClosed) {
			s.rrSilent(c)
		}
	}
}

// rrDead is the action of D1 on the node of t: its Q1 signs are dropped, its dials
// in flight cancelled (they go to the retry), its waits closed (CANCEL).
func (s *URLTest) rrDead(t *rrTrack) {
	t.dropPending()
	t.deadAt.Store(rrClock(time.Now()))
	t.clearSuspicion()
	if killed := t.kill(); killed > 0 {
		s.logger.Info("round_robin: ", t.tag, " is dead: ", killed, " unfinished exchanges through it cancelled")
	}
}

// lx:end SPEC 138
