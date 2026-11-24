package group

// lx:begin SPEC 138 round_robin unfinished connections and pending failures per node
//
// Every slot of a round_robin pool carries a tracker of its node (rrTrack). It lives
// as long as the node holds a slot: setSlots and replaceSlot hand it over to the
// same node and give a newcomer a fresh one; a node that leaves takes its pending
// failures with it. Connections that are still running keep a reference to the
// tracker they registered in; a tracker out of the pool is consulted by nobody.
//
// Unfinished connections (a node that accepts and stays silent gives no failure):
//   - dials in flight: DialContext/ListenPacket of the node has not returned. Each
//     such dial runs on its own cancellable context (rrDialStart), derived from the
//     caller's, never from the group's;
//   - connections whose client sent something and got no first byte yet (TCP, a
//     connected UDP flow, a UDP flow).
//
// Both lists are ordered by their time, so the oldest entry of each is one atomic
// load (oldest). Each pick of a slot checks it (rrTrack.attention): an entry older
// than rrHangAfter is a reason for a check of the node (rrHang with all its filters),
// looked at no more often than rrHangLook per slot. A dead verdict on the node
// cancels its dials in flight — they go the usual way of a failed open, one retry
// through the next live slot, with no new check (errRRStruck) — and closes its
// connections that wait for a first byte; a connection that got one is left alone.
// The "own network" verdict and a freeze cancel and close nothing.
//
// Pending failures: a failure of a connection (a dial that did not open, a close
// without a byte) waits for the verdict on the node it failed on, at most
// rrPendingMax per node (the oldest goes) and rrPendingTTL long. "Alive" comes from
// the first of: the first byte of any connection through the node after the
// failure, an answer of a probe of the node that started after it, history of the
// node written after it. Every pending failure older than that proof then gets what
// it is due (rrFailure.settle). The dead verdict drops the node's pending failures;
// a freeze voids every pending failure older than it (rrHeal.lastFreeze). A failure
// that needs a probe asks for one through checkAfter: the next pick of the node at
// or after that time starts the check, unless the verdict came meanwhile.
//
// A failure that puts the site under suspicion (a close without a byte, a failed
// open of a direct or endpoint leaf, a failed retry) starts a verdict probe at
// once: a probe of that node alone, with the group's URL and timeout, outside the
// check slot and its gap — at most one in flight per node and one per node every
// rrVerdictGap, none while frozen. An answer writes the node's history and settles
// its pending failures; silence starts the check with a control on the usual
// terms. A failed open of a proxy leaf puts the node under suspicion and goes to
// the check with a control at once.
//
// A slot under suspicion (rrTrack.suspect): an unfinished connection through it is
// older than rrHangAfter and the node has not proved alive since that connection
// started (proved: the start of a later connection that got its first byte, the
// start of a probe that answered, history written later). The pick skips such a
// slot like a dead one while another slot is fine; the suspicion lifts with a
// proof, the verdict of a check (dead: its connections end), or when such a
// connection ends. It changes no history, shifts no key and counts no failure.

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

const (
	// rrPendingMax bounds the pending failures of one node; the oldest goes first.
	rrPendingMax = 64
	// rrPendingTTL — a pending failure without a verdict this long is dropped.
	rrPendingTTL = 60 * time.Second
	// rrVerdictWait — a failure that puts the site under suspicion asks for a probe
	// of its node at the next pick of the node this long after it, if no first byte
	// through the node has given the verdict by then; a check that could not start
	// asks again this long later.
	rrVerdictWait = time.Second
	// rrHangLook — the unfinished connections of a slot are looked at by a pick no
	// more often than this.
	rrHangLook = time.Second
	// rrVerdictGap — a node gets a verdict probe no more often than this.
	rrVerdictGap = 3 * time.Second
)

// rrPendMode is how a failure asks for the verdict on its node (rrPend).
type rrPendMode int

const (
	rrCheckNow   rrPendMode = iota // the check with a control at once: the node is under suspicion
	rrVerdictNow                   // a verdict probe of the node alone at once: the site is under suspicion
)

// errRRStruck is the cause of a dial in flight cancelled by the dead verdict on its node.
var errRRStruck = E.New("round_robin: the node is struck out as dead")

// rrUnwatched reports a connection that takes no part in the self-heal: a
// connection test (SPEC 139) and a delay measurement through the group
// (urltest.URLTest of an outer group, the clash API). It is not wrapped, tracked,
// counted or shifted and starts no check.
func rrUnwatched(ctx context.Context) bool {
	return lxtrace.IsProbe(ctx) || urltest.IsMeasureLx(ctx)
}

// rrTime is the time of an rrClock mark (with the monotonic reading).
func rrTime(clock int64) time.Time {
	return rrClockBase.Add(time.Duration(clock - 1))
}

// rrKillable is an unfinished connection the dead verdict ends.
type rrKillable interface {
	rrKill()
}

// rrTrackEntry is one unfinished connection in a list of a tracker; it is embedded
// in its owner, so tracking allocates nothing.
type rrTrackEntry struct {
	prev, next *rrTrackEntry
	list       *rrTrackList
	since      int64 // rrClock
	owner      rrKillable
}

// rrTrackList is a list of unfinished connections ordered by since; the caller
// holds the tracker's access.
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

// newestBefore returns the since of the newest entry not newer than cutoff; found
// is false for none.
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

// rrPending is a failure waiting for the verdict on a node: as the node it failed
// on (A), or as the node of its retry that failed too (retry).
type rrPending struct {
	failure *rrFailure
	retry   bool
	at      int64 // rrClock of the failure
}

// rrTrack is the tracker of one pool member (see the file header).
type rrTrack struct {
	tag        string
	access     sync.Mutex
	dials      rrTrackList // dials in flight
	waits      rrTrackList // the client sent, no first byte yet
	pending    []rrPending // ordered by at
	pendingN   atomic.Int32
	checkAfter atomic.Int64 // rrClock from which a pick of the node starts the check; 0 none
	hangLook   atomic.Int64 // rrClock before which a pick does not look at the unfinished connections
	// proved is the rrClock from which the node is known alive: the start of the
	// latest connection that got its first byte, of the latest probe that answered,
	// the time of history written by another run.
	proved atomic.Int64
	// suspectSince is the since of the newest unfinished connection older than
	// rrHangAfter not covered by proved, found by the last look (rrOnPick); 0 none.
	suspectSince atomic.Int64
	verdictBusy  atomic.Bool  // a verdict probe of the node is in flight
	verdictLast  atomic.Int64 // rrClock of the start of the last verdict probe
}

// markProved records that the node proved alive from at on.
func (t *rrTrack) markProved(at int64) {
	for {
		old := t.proved.Load()
		if old >= at || t.proved.CompareAndSwap(old, at) {
			return
		}
	}
}

// suspect reports, with a few atomic loads, whether the pick skips the slot: an
// unfinished connection through it is older than rrHangAfter and the node has not
// proved alive since it started. It allocates nothing and takes no lock.
func (t *rrTrack) suspect(now int64) bool {
	cutoff := now - int64(rrHangAfter)
	oldest := t.dials.oldest.Load()
	if w := t.waits.oldest.Load(); w != 0 && (oldest == 0 || w < oldest) {
		oldest = w
	}
	if oldest == 0 || oldest > cutoff {
		return false
	}
	proved := t.proved.Load()
	if proved < oldest {
		return true
	}
	// A newer unfinished connection, found by the last look, may outlast the proof.
	since := t.suspectSince.Load()
	return since != 0 && since <= cutoff && proved < since
}

func newRRTrack(tag string) *rrTrack {
	t := &rrTrack{tag: tag}
	t.proved.Store(math.MinInt64) // never: an rrClock mark may be below zero
	return t
}

// track registers an unfinished connection in list.
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

// untrack removes an unfinished connection; a no-op for one not registered.
func (t *rrTrack) untrack(e *rrTrackEntry) {
	t.access.Lock()
	defer t.access.Unlock()
	if e.list != nil {
		e.list.remove(e)
	}
}

// attention reports, with a few atomic loads, whether a pick at now has work: a
// pending failure due for a check, or an unfinished connection older than
// rrHangAfter not looked at lately. It allocates nothing and takes no lock.
func (t *rrTrack) attention(now int64) bool {
	if due := t.checkAfter.Load(); due != 0 && now >= due {
		return true
	}
	if now < t.hangLook.Load() {
		return false
	}
	cutoff := now - int64(rrHangAfter)
	if d := t.dials.oldest.Load(); d != 0 && d <= cutoff {
		return true
	}
	w := t.waits.oldest.Load()
	return w != 0 && w <= cutoff
}

// newestHung returns the since of the newest unfinished connection not newer than
// cutoff, over both lists; found is false for none.
func (t *rrTrack) newestHung(cutoff int64) (int64, bool) {
	t.access.Lock()
	defer t.access.Unlock()
	newest, found := t.dials.newestBefore(cutoff)
	if w, waiting := t.waits.newestBefore(cutoff); waiting && (!found || w > newest) {
		newest, found = w, true
	}
	return newest, found
}

// kill ends every unfinished connection of the node: the dead verdict; the
// suspicion of the slot goes with them. A dial is
// marked struck under access, so a dial that fails on its own meanwhile still
// knows the verdict (rrDial.failed).
func (t *rrTrack) kill() int {
	t.access.Lock()
	owners := t.dials.drain(nil)
	for _, owner := range owners {
		owner.(*rrDial).struck.Store(true)
	}
	owners = t.waits.drain(owners)
	t.suspectSince.Store(0)
	t.access.Unlock()
	for _, owner := range owners {
		owner.rrKill()
	}
	return len(owners)
}

// addPending records a failure waiting for the verdict on the node.
func (t *rrTrack) addPending(p rrPending) {
	t.access.Lock()
	defer t.access.Unlock()
	t.purge(p.at)
	if len(t.pending) >= rrPendingMax {
		copy(t.pending, t.pending[1:])
		t.pending = t.pending[:len(t.pending)-1]
	}
	t.pending = append(t.pending, p)
	t.pendingN.Store(int32(len(t.pending)))
}

// purge drops the pending failures older than rrPendingTTL at now; the caller
// holds access.
func (t *rrTrack) purge(now int64) {
	expired := 0
	for expired < len(t.pending) && now-t.pending[expired].at > int64(rrPendingTTL) {
		expired++
	}
	if expired > 0 {
		t.pending = append(t.pending[:0], t.pending[expired:]...)
	}
}

// takeAlive removes and returns the pending failures that the proof "alive at
// before" settles: those that happened before it, after void (the last freeze),
// and not older than rrPendingTTL at now. The failures not newer than void are
// dropped.
func (t *rrTrack) takeAlive(before, void, now int64) []rrPending {
	t.access.Lock()
	defer t.access.Unlock()
	t.purge(now)
	var settled []rrPending
	kept := t.pending[:0]
	for _, p := range t.pending {
		switch {
		case p.at <= void:
		case p.at < before:
			settled = append(settled, p)
		default:
			kept = append(kept, p)
		}
	}
	for i := len(kept); i < len(t.pending); i++ {
		t.pending[i] = rrPending{} // no reference to a settled failure
	}
	t.pending = kept
	t.pendingN.Store(int32(len(kept)))
	if len(kept) == 0 {
		t.checkAfter.Store(0)
	}
	return settled
}

// dropPending drops every pending failure: the dead verdict, the node left the pool.
func (t *rrTrack) dropPending() {
	t.access.Lock()
	defer t.access.Unlock()
	t.pending = nil
	t.pendingN.Store(0)
	t.checkAfter.Store(0)
}

// deferCheck asks the pick of the node at or after at to start the check.
func (t *rrTrack) deferCheck(at int64) {
	for {
		due := t.checkAfter.Load()
		if due != 0 && due <= at {
			return
		}
		if t.checkAfter.CompareAndSwap(due, at) {
			return
		}
	}
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
	struck atomic.Bool // the dead verdict took it off the list (set under track.access)
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
// context; it reports whether the dead verdict on the node cancelled it.
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
// release the context). A tracked dial is always wrapped on success (rrWatch).
func (s *URLTest) rrTrackDial(ctx context.Context, track *rrTrack, member adapter.Outbound, leaf adapter.Outbound, since int64) (context.Context, *rrDial) {
	if track == nil || rrUnwatched(ctx) || rrNestedURLTest(member) || rrDirectLeaf(leaf) {
		return ctx, nil
	}
	dialCtx, cancel := context.WithCancelCause(ctx)
	dial := &rrDial{track: track, ctx: dialCtx, cancel: cancel}
	track.track(&track.dials, &dial.entry, since, dial)
	return dialCtx, dial
}

// rrDialStart tracks the dial of the pick (DialContext, ListenPacket) through its
// chain leaf; it returns the context to dial on and records the dial in picked.
// A no-op outside round_robin and for a pick outside the pool.
func (s *URLTest) rrDialStart(ctx context.Context, leaf adapter.Outbound, picked *rrPick) context.Context {
	if s.balancer == nil || picked.track == nil || picked.track.tag != leaf.Tag() {
		return ctx
	}
	member, loaded := s.outbound.Outbound(leaf.Tag())
	if !loaded {
		return ctx
	}
	ctx, picked.dial = s.rrTrackDial(ctx, picked.track, member, leaf, rrClock(picked.start))
	return ctx
}

// --- the pick --------------------------------------------------------------------

// rrWire connects the round_robin pick to the group: it skips the slots that failed
// their last probe and looks after the unfinished connections and the pending
// failures of the slot it picks.
func (s *URLTest) rrWire() {
	s.balancer.alive = s.slotAlive
	s.balancer.onPick = s.rrOnPick
}

// rrOnPick is the slow path of a pick whose slot has work (rrTrack.attention): a
// pending failure due for a check, or an unfinished connection older than
// rrHangAfter. Such a connection with no proof of the node since it started makes
// the slot suspect; the check from a reason (rrHang) or, when that cannot run now,
// a verdict probe of the node decides.
func (s *URLTest) rrOnPick(ctx context.Context, tag string, t *rrTrack, now int64) {
	if rrUnwatched(ctx) || s.group.ctx.Err() != nil {
		return
	}
	if due := t.checkAfter.Load(); due != 0 && now >= due && t.checkAfter.CompareAndSwap(due, 0) {
		s.rrPendingDue(tag, t, now)
	}
	look := t.hangLook.Load()
	if now < look || !t.hangLook.CompareAndSwap(look, now+int64(rrHangLook)) {
		return
	}
	since, found := t.newestHung(now - int64(rrHangAfter))
	if !found || t.proved.Load() >= since {
		t.suspectSince.Store(0)
		return
	}
	node, loaded := s.outbound.Outbound(tag)
	if !loaded {
		return
	}
	if leaf := RealTag(node, N.NetworkTCP); leaf != "" {
		if history := s.group.history.LoadURLTestHistory(leaf); history != nil {
			if at := rrClock(history.Time); at > since {
				t.markProved(at) // another run answered for the node since
				t.suspectSince.Store(0)
				return
			}
		}
	}
	t.suspectSince.Store(since)
	if s.balancer.heal.hangReady() && s.rrHang(tag, rrTime(since), false) {
		return
	}
	s.rrVerdictProbe(node, t, nil) // the check cannot run now: the node alone answers for itself
}

// rrPendingDue is a pick of a node whose pending failures asked for a check: the
// history written since settles them; otherwise the check starts, or asks again
// rrVerdictWait later when it cannot start now.
func (s *URLTest) rrPendingDue(tag string, t *rrTrack, now int64) {
	node, loaded := s.outbound.Outbound(tag)
	if !loaded {
		return
	}
	if leaf := RealTag(node, N.NetworkTCP); leaf != "" {
		if history := s.group.history.LoadURLTestHistory(leaf); history != nil {
			t.markProved(rrClock(history.Time))
			s.rrSettleAlive(t, rrClock(history.Time))
		}
	}
	if t.pendingN.Load() == 0 || s.balancer.heal.frozen() {
		return
	}
	if !s.rrStartCheck(node, false) {
		t.deferCheck(now + int64(rrVerdictWait))
	}
}

// rrPend records failure as pending on the pool member node (retry: as the node of
// its retry) and asks for the verdict as mode says. When neither the check nor the
// verdict probe can start now, the next pick of the node after rrVerdictWait asks
// again unless a first byte through the node settles the failure first.
func (s *URLTest) rrPend(node adapter.Outbound, failure *rrFailure, retry bool, mode rrPendMode) {
	t := s.balancer.trackOf(node.Tag())
	if t == nil {
		return
	}
	at := rrClock(time.Now())
	t.addPending(rrPending{failure: failure, retry: retry, at: at})
	switch mode {
	case rrCheckNow:
		if s.rrStartCheck(node, false) {
			return
		}
	case rrVerdictNow:
		if s.rrVerdictProbe(node, t, func() {
			if !s.rrStartCheck(node, false) {
				t.deferCheck(rrClock(time.Now()) + int64(rrVerdictWait))
			}
		}) {
			return
		}
	}
	t.deferCheck(at + int64(rrVerdictWait))
}

// rrVerdictProbe probes the pool member node alone, outside the check slot and its
// gap: at most one in flight per node and one per node every rrVerdictGap, none
// while frozen. An answer writes its history, proves the node alive from the
// probe's start and settles its failures pending since before it; on silence
// silent runs (nil: nothing — the check from a reason decides later). It reports
// whether the probe started.
func (s *URLTest) rrVerdictProbe(node adapter.Outbound, t *rrTrack, silent func()) bool {
	heal := &s.balancer.heal
	if t == nil || heal.frozen() || s.group.ctx.Err() != nil {
		return false
	}
	now := rrClock(time.Now())
	if last := t.verdictLast.Load(); last != 0 && now-last < int64(rrVerdictGap) {
		return false
	}
	if !t.verdictBusy.CompareAndSwap(false, true) {
		return false
	}
	t.verdictLast.Store(now)
	heal.verdicts.Add(1)
	go func() {
		defer heal.verdicts.Add(-1)
		defer t.verdictBusy.Store(false)
		ctx, cancel := context.WithCancel(s.group.ctx)
		defer cancel()
		probe := s.group.rrProbeNode(ctx, node, false)
		if heal.aborted(ctx) || probe.leaf == "" {
			return
		}
		if probe.answered {
			s.group.rrStore(probe)
			s.rrSettleProbe(probe)
			return
		}
		if silent != nil {
			silent()
		}
	}()
	return true
}

// rrSettleAlive is the verdict "alive at before" on the node of t: every pending
// failure before it gets what it is due.
func (s *URLTest) rrSettleAlive(t *rrTrack, before int64) {
	if t == nil || t.pendingN.Load() == 0 {
		return
	}
	heal := &s.balancer.heal
	if heal.frozen() {
		return
	}
	for _, p := range t.takeAlive(before, heal.lastFreeze.Load(), rrClock(time.Now())) {
		if p.retry {
			p.failure.setRetryAlive()
		} else {
			p.failure.setAlive()
		}
	}
}

// rrSettleProbe is a probe of the pool member tag that answered: its pending
// failures before the probe started are settled.
func (s *URLTest) rrSettleProbe(probe rrProbe) {
	if probe.answered && !probe.fresh {
		t := s.balancer.trackOf(probe.member)
		if t != nil {
			t.markProved(rrClock(probe.started))
		}
		s.rrSettleAlive(t, rrClock(probe.started))
	}
}

// rrDead is the dead verdict on the pool member tag: its pending failures are
// dropped, its dials in flight cancelled and its connections waiting for a first
// byte closed. t is its tracker, taken before its slot could be replaced.
func (s *URLTest) rrDead(t *rrTrack) {
	if t == nil {
		return
	}
	t.dropPending()
	if s.balancer.heal.frozen() {
		return
	}
	if killed := t.kill(); killed > 0 {
		s.logger.Info("round_robin: ", t.tag, " is dead: ", killed, " unfinished connections through it cancelled")
	}
}

// lx:end SPEC 138
