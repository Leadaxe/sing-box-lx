package group

// lx:begin SPEC 138 round_robin key shift, slot counters and quarantine (stage 2)
//
// Key shift (§3.5). A destination may be unreachable through one live node and
// reachable through another. The group remembers that as one number per key, the
// shift: the key of the table is the sticky key plus the destination (the domain,
// else the destination IP) unless the sticky key holds it already, so one dead
// site does not move a whole application off its node; the slot is still counted
// from the sticky key, (hash + shift) % pool. The shift grows when a failure (a
// dial that did not open, or a connection closed without a byte) hit a node that
// proved alive after the failure (urltest_rr_track_lx.go): the first byte of a
// connection through it that started after the failure, an answer of a probe of it
// that started after the failure, or its history written after the failure. Until then the failure is
// pending on the node; the dead verdict drops it, a freeze voids it. When the own
// network is gone no byte comes, and nothing shifts. A
// shifted key whose slot is dead goes to the next live slot going round, the same
// one its retry went to. A connection remembers the shift its key was picked with;
// its failure shifts the key only while the shift is still that one, so parallel
// connections of a key failing on one node shift it once. A failure on a node the
// key passed already shifts it on as any other (the pool may have been rebuilt
// under the key). A key that failed on as many different nodes of the current pool
// as there are live slots is marked "address problem" (the nodes it passed that
// left the pool do not count): it goes back to its own slot and gets no retries,
// shifts or checks by its failures until the next scheduled run, manual run or
// network change. Unmarked shifts live until restart. The table holds 1024 keys
// and drops the oldest one; the shift belongs to the slot, not to the node.
// Without stickiness there is no table.
//
// Slot counters (§3.6). Per pool member: connections and confirmed failures since
// the node took its slot. A failure is confirmed when (1) the retry of the same
// connection got the first byte — one to the node that failed the dial, counted
// once that node proved alive after the failure; (2) a
// shifted key got the first byte on another slot — one to every node it passed,
// once, and never twice within one connection. An address-problem key confirms
// nothing, nor does a node without history (dead). At 256 connections both
// counters halve; the dead verdict drops them. A node with at least 8 connections,
// 3 confirmed failures, a failure share of at least 5% and above twice the median
// share of the other pool members with at least 8 connections goes to quarantine (no such
// neighbour, no quarantine): the group skips
// its slot (slotAlive) and refills it like a dead one, the node is no candidate and
// no control; its history stays. The scheduled run, a manual run and a network
// change lift every quarantine. Nothing is counted or shifted while the self-heal
// is frozen; a connection test (SPEC 139) leaves all of this alone.

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const (
	// rrShiftTableSize bounds the shift table; the oldest key goes first.
	rrShiftTableSize = 1024
	// rrCountHalveAt halves both slot counters when the connections reach it.
	rrCountHalveAt = 256
	// rrQuarantineMinFails is the least number of confirmed failures for quarantine.
	rrQuarantineMinFails = 3
	// rrQuarantineMinShare is the least failure share for quarantine: with no
	// failures at the neighbours the median is 0, and "above twice the median"
	// alone would hold for any share.
	rrQuarantineMinShare = 0.05
	// rrQuarantineMinConns is the least number of connections of a node whose share
	// counts: for the quarantine candidate and for the median of its neighbours.
	rrQuarantineMinConns = 8
	// rrQuarantineWait bounds how long a quarantine waits for the check in flight
	// and the gap after the previous one before it gives up the refill.
	rrQuarantineWait = rrHealGap + C.TCPTimeout
)

// rrKey identifies a connection for the shift table.
type rrKey struct {
	sticky uint64     // hash of the sticky key: the slot
	table  uint64     // hash of the table key: the sticky key plus the destination
	domain string     // the destination domain, for the log
	addr   netip.Addr // the destination IP without a domain, for the log
	port   uint16     // the destination port, for the log when the sticky key holds it
	valid  bool       // stickiness is on
}

// label is the destination of the key, for the log: an IP destination carries its
// port when the sticky key holds the port (dest_port).
func (k rrKey) label(stickyHash []string) string {
	if k.domain != "" {
		return k.domain
	}
	if k.addr.IsValid() {
		if k.port != 0 && common.Contains(stickyHash, C.URLTestStickyDestPort) {
			return netip.AddrPortFrom(k.addr, k.port).String()
		}
		return k.addr.String()
	}
	return ""
}

// rrPick is what the pick of a connection hands to its dial: the shift-table key,
// the shift of the key at the pick and the start of the connection.
type rrPick struct {
	key   rrKey
	shift int
	start time.Time
	track *rrTrack // the tracker of the picked slot; nil for a pick outside the pool
	dial  *rrDial  // the dial of the pick in flight, when tracked (rrDialStart)
	// detour: the slot the key would take was under suspicion and the pick went to
	// the next one instead; a failure there shifts no key — the shift counts from
	// the slot the key takes without a suspicion, which was not tried.
	detour bool
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// fnvString continues an FNV-1a hash over s (hashKey is fnvString from the offset).
func fnvString(hash uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		hash ^= uint64(s[i])
		hash *= fnvPrime64
	}
	return hash
}

func fnvBytes(hash uint64, b []byte) uint64 {
	for _, c := range b {
		hash ^= uint64(c)
		hash *= fnvPrime64
	}
	return hash
}

// fnvAddr continues an FNV-1a hash over the text form of addr, without a string.
func fnvAddr(hash uint64, addr netip.Addr) uint64 {
	var scratch [64]byte
	return fnvBytes(hash, addr.AppendTo(scratch[:0]))
}

// stickyHashOf is hashKey(b.stickyKey(ctx, destination)) without building the key.
func (b *balancer) stickyHashOf(metadata *adapter.InboundContext, destination M.Socksaddr) uint64 {
	hash := uint64(fnvOffset64)
	for i, component := range b.stickyHash {
		if i > 0 {
			hash = fnvString(hash, "\x00")
		}
		switch component {
		case C.URLTestStickySourceIP:
			if metadata != nil && metadata.Source.Addr.IsValid() {
				hash = fnvAddr(hash, metadata.Source.Addr)
			}
		case C.URLTestStickyDestIP:
			if destination.Addr.IsValid() {
				hash = fnvAddr(hash, destination.Addr)
			}
		case C.URLTestStickyDestPort:
			if destination.Port != 0 {
				var scratch [8]byte
				hash = fnvBytes(hash, strconv.AppendUint(scratch[:0], uint64(destination.Port), 10))
			}
		default:
			hash = fnvString(hash, stickyComponent(component, metadata, destination))
		}
	}
	return hash
}

// rrTarget returns the destination of a connection — the domain, else the IP —
// and whether the sticky key already holds it.
func (b *balancer) rrTarget(metadata *adapter.InboundContext, destination M.Socksaddr) (string, netip.Addr, bool) {
	var domain string
	if metadata != nil && metadata.Domain != "" {
		domain = metadata.Domain
	} else {
		domain = destination.Fqdn
	}
	if domain != "" {
		return domain, netip.Addr{}, common.Contains(b.stickyHash, C.URLTestStickyDomain)
	}
	if destination.Addr.IsValid() {
		return "", destination.Addr, common.Contains(b.stickyHash, C.URLTestStickyDestIP)
	}
	return "", netip.Addr{}, true
}

// rrKeyOf returns the shift-table key of a connection; invalid without stickiness.
// It builds no strings: pick computes it for every connection.
func (b *balancer) rrKeyOf(ctx context.Context, destination M.Socksaddr) rrKey {
	if len(b.stickyHash) == 0 {
		return rrKey{}
	}
	metadata := adapter.ContextFrom(ctx)
	sticky := b.stickyHashOf(metadata, destination)
	key := rrKey{sticky: sticky, table: sticky, port: destination.Port, valid: true}
	domain, addr, inKey := b.rrTarget(metadata, destination)
	key.domain, key.addr = domain, addr
	if !inKey {
		hash := fnvString(sticky, "\x00\x00")
		if domain != "" {
			key.table = fnvString(hash, domain)
		} else {
			key.table = fnvAddr(hash, addr)
		}
	}
	return key
}

// --- shift table -----------------------------------------------------------------

type rrPassedNode struct {
	tag      string
	credited bool // its failure is confirmed already (§3.6.2)
	// silent: the key passed the node by silence alone (a silent site through a
	// live node, a timeout of an open that went to the site): it shifts the key but
	// confirms no failure — silence is ambiguous (a slow site, a long poll) and must
	// not press towards quarantine.
	silent bool
}

type rrShiftEntry struct {
	shift  int
	marked bool           // address problem
	passed []rrPassedNode // the nodes the key failed on, in order
}

// rrShiftTable is the shift table of a group: at most rrShiftTableSize keys, the
// oldest dropped first.
type rrShiftTable struct {
	access  sync.RWMutex // pick reads under RLock
	entries map[uint64]*rrShiftEntry
	ring    []uint64 // insertion order
	next    int      // the oldest entry of a full ring
	size    atomic.Int32
}

// entry returns the entry of key, creating it; the caller holds access.
func (t *rrShiftTable) entry(key uint64) *rrShiftEntry {
	if e := t.entries[key]; e != nil {
		return e
	}
	if t.entries == nil {
		t.entries = make(map[uint64]*rrShiftEntry)
	}
	if len(t.ring) < rrShiftTableSize {
		t.ring = append(t.ring, key)
	} else {
		delete(t.entries, t.ring[t.next])
		t.ring[t.next] = key
		t.next = (t.next + 1) % rrShiftTableSize
	}
	e := &rrShiftEntry{}
	t.entries[key] = e
	t.size.Store(int32(len(t.entries)))
	return e
}

// shiftOf returns the shift of key; 0 for an unknown or marked one.
func (t *rrShiftTable) shiftOf(key uint64) int {
	if t.size.Load() == 0 {
		return 0
	}
	t.access.RLock()
	defer t.access.RUnlock()
	if e := t.entries[key]; e != nil && !e.marked {
		return e.shift
	}
	return 0
}

// marked reports whether key is an address problem.
func (t *rrShiftTable) marked(key uint64) bool {
	if t.size.Load() == 0 {
		return false
	}
	t.access.RLock()
	defer t.access.RUnlock()
	e := t.entries[key]
	return e != nil && e.marked
}

// unmarkAll lifts every address-problem mark.
func (t *rrShiftTable) unmarkAll() {
	t.access.Lock()
	defer t.access.Unlock()
	for _, e := range t.entries {
		e.marked = false
	}
}

// markCredited records that the failure of key on tag is confirmed already.
func (t *rrShiftTable) markCredited(key uint64, tag string) {
	t.access.Lock()
	defer t.access.Unlock()
	if e := t.entries[key]; e != nil {
		for i := range e.passed {
			if e.passed[i].tag == tag {
				e.passed[i].credited = true
			}
		}
	}
}

// creditable returns the nodes key passed whose failures the first byte through
// success confirms (§3.6.2, case 2), and marks them confirmed. A marked key
// confirms nothing; nor does a first byte through a node the key passed. credited
// is a node the same connection has confirmed already (case 1): it is marked
// confirmed and not returned again.
func (t *rrShiftTable) creditable(key uint64, success string, credited string) []string {
	if t.size.Load() == 0 {
		return nil
	}
	t.access.Lock()
	defer t.access.Unlock()
	e := t.entries[key]
	if e == nil || e.marked {
		return nil
	}
	for _, passed := range e.passed {
		if passed.tag == success {
			return nil
		}
	}
	var tags []string
	for i := range e.passed {
		if !e.passed[i].credited {
			e.passed[i].credited = true
			if e.passed[i].tag != credited && !e.passed[i].silent {
				tags = append(tags, e.passed[i].tag)
			}
		}
	}
	return tags
}

// firstLiveFrom returns the first usable slot going round from idx, idx included.
func (b *balancer) firstLiveFrom(slots []slot, idx int, now int64, strict bool) (int, bool) {
	n := len(slots)
	for step := 0; step < n; step++ {
		i := (idx + step) % n
		if b.usable(slots[i], now, strict) {
			return i, true
		}
	}
	return 0, false
}

func slotIndex(slots []slot, tag string) int {
	for i, entry := range slots {
		if entry.tag == tag {
			return i
		}
	}
	return -1
}

// inPool reports whether tag holds a slot.
func (b *balancer) inPool(tag string) bool {
	return slotIndex(b.slotsSnapshot(), tag) >= 0
}

// rrShiftOn shifts key past node, which the key failed on while node is alive.
// expected is the shift the failed connection was picked with: another shift
// since then means a parallel connection of the key has moved it already, and
// nothing changes. A node the key passed already shifts it on as any other.
// credited records the failure as confirmed already. It returns the shift of the
// key and whether this call changed it.
func (s *URLTest) rrShiftOn(key rrKey, expected int, node string, credited bool, silent bool) (int, bool) {
	b := s.balancer
	if !key.valid || b.heal.frozen() || s.group.ctx.Err() != nil {
		return 0, false
	}
	slots := b.slotsSnapshot()
	n := len(slots)
	failed := slotIndex(slots, node)
	if failed < 0 {
		return 0, false
	}
	live := 0
	for _, entry := range slots {
		if s.slotAlive(entry.tag) {
			live++
		}
	}
	if live == 0 {
		live = n
	}
	table := &b.memory.shifts
	table.access.Lock()
	e := table.entry(key.table)
	if e.marked || e.shift != expected {
		shift := e.shift
		table.access.Unlock()
		return shift, false
	}
	base := int(key.sticky % uint64(n))
	current := (base + e.shift) % n
	e.shift += (failed-current+n)%n + 1
	// The circle counts the different nodes of the current pool the key passed; a
	// node that left the pool is dropped.
	passed := e.passed[:0]
	known := false
	for _, entry := range e.passed {
		if slotIndex(slots, entry.tag) < 0 {
			continue
		}
		if entry.tag == node {
			known = true
			entry.credited = entry.credited || credited
			entry.silent = entry.silent && silent // an active failure there counts
		}
		passed = append(passed, entry)
	}
	if !known {
		passed = append(passed, rrPassedNode{tag: node, credited: credited, silent: silent})
	}
	e.passed = passed
	marked := len(e.passed) >= live
	if marked {
		e.marked, e.shift, e.passed = true, 0, nil
	}
	shift := e.shift
	table.access.Unlock()
	if marked {
		s.logger.Info("round_robin: ", key.label(b.stickyHash), " is unreachable through every node of the pool; marked as an address problem, back to its own slot")
		return shift, true
	}
	s.logger.Debug("round_robin: ", key.label(b.stickyHash), " failed through live ", node, "; key shifted by ", shift)
	return shift, true
}

// --- slot counters and quarantine ----------------------------------------------

type rrSlotCount struct {
	conns uint32
	fails uint32
	// keys is the ring of the table keys whose failure counted already: a node is
	// credited at most once per key, so one site closed only through the node (a
	// protocol with no destination mark: SSH, a SOCKS server that closes with no
	// code) does not take it to quarantine; it needs failures of three keys.
	keys [rrCountKeys]uint64
	next int
}

// rrCountKeys bounds the ring of keys of a slot counter.
const rrCountKeys = 16

// creditedKey reports whether key counted already, and records it when not.
func (c *rrSlotCount) creditedKey(key uint64) bool {
	for _, known := range c.keys {
		if known == key {
			return true
		}
	}
	c.keys[c.next] = key
	c.next = (c.next + 1) % rrCountKeys
	return false
}

// rrMemory is the stage-2 state of a round_robin group; it lives in the balancer.
type rrMemory struct {
	shifts     rrShiftTable
	access     sync.Mutex // guards counts
	counts     map[string]*rrSlotCount
	quarantine atomic.Pointer[map[string]bool]
}

// count returns the counters of tag, creating them; the caller holds access.
func (m *rrMemory) count(tag string) *rrSlotCount {
	if m.counts == nil {
		m.counts = make(map[string]*rrSlotCount)
	}
	c := m.counts[tag]
	if c == nil {
		c = &rrSlotCount{}
		m.counts[tag] = c
	}
	return c
}

// share returns the failure share of tag and whether it counts: a node with fewer
// than rrQuarantineMinConns connections (a fresh replacement, a node just out of
// quarantine) says nothing yet. The caller holds access.
func (m *rrMemory) share(tag string) (float64, bool) {
	c := m.counts[tag]
	if c == nil || c.conns == 0 {
		return 0, false
	}
	return float64(c.fails) / float64(c.conns), c.conns >= rrQuarantineMinConns
}

// isQuarantined reports whether tag is in quarantine.
func (b *balancer) isQuarantined(tag string) bool {
	set := b.memory.quarantine.Load()
	return set != nil && (*set)[tag]
}

// rrCountConn counts a connection of the pool member tag. The membership is checked
// under access: rrSlotsChanged drops the counters of a node that left the pool
// under the same lock, so no counter outlives its node's slot.
func (b *balancer) rrCountConn(tag string, heal *rrHeal) {
	if heal.frozen() {
		return
	}
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	if !b.inPool(tag) {
		return
	}
	c := m.count(tag)
	c.conns++
	if c.conns >= rrCountHalveAt {
		c.conns /= 2
		c.fails /= 2
	}
}

// rrByteSince reports whether a connection through the pool member tag that
// started after clock (rrClock) got its first byte: the node's proof mark
// (rrTrack.proved, which a probe that answered moves too). A node out of the pool
// has no mark.
func (b *balancer) rrByteSince(tag string, clock int64) bool {
	t := b.trackOf(tag)
	return t != nil && t.proved.Load() > clock
}

// rrQuarantineVerdict is the outcome of one confirmed failure.
type rrQuarantineVerdict struct {
	quarantined bool
	fails       uint32
	conns       uint32
	share       float64
	median      float64
}

// rrCountFail counts a confirmed failure of the pool member tag and quarantines it
// when it fails noticeably more often than the rest of the pool.
func (b *balancer) rrCountFail(tag string, key rrKey) rrQuarantineVerdict {
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	slots := b.slotsSnapshot() // under access, as in rrCountConn
	if slotIndex(slots, tag) < 0 {
		return rrQuarantineVerdict{}
	}
	c := m.count(tag)
	if key.valid && c.creditedKey(key.table) {
		return rrQuarantineVerdict{} // this key counted against the node already
	}
	c.fails++
	share, counts := m.share(tag)
	verdict := rrQuarantineVerdict{fails: c.fails, conns: c.conns, share: share}
	if !counts || c.fails < rrQuarantineMinFails || share < rrQuarantineMinShare || b.isQuarantined(tag) {
		return verdict
	}
	others := make([]float64, 0, len(slots))
	for _, entry := range slots {
		if entry.tag == tag || entry.tag == "" {
			continue
		}
		if other, counts := m.share(entry.tag); counts {
			others = append(others, other)
		}
	}
	if len(others) == 0 {
		return verdict // nobody busy enough to compare with
	}
	verdict.median = rrMedian(others)
	if verdict.share <= 2*verdict.median {
		return verdict
	}
	next := make(map[string]bool)
	if set := m.quarantine.Load(); set != nil {
		for quarantined := range *set {
			next[quarantined] = true
		}
	}
	next[tag] = true
	m.quarantine.Store(&next)
	verdict.quarantined = true
	return verdict
}

func rrMedian(values []float64) float64 {
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

// rrSlotsChanged drops the counters of the nodes that left the pool: a node that
// takes a slot starts from zero. Called after the slots change, without access held.
func (b *balancer) rrSlotsChanged() {
	slots := b.slotsSnapshot()
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	for tag := range m.counts {
		if slotIndex(slots, tag) < 0 {
			delete(m.counts, tag)
		}
	}
}

// rrResetMarks lifts the address-problem marks and the quarantine: the scheduled
// run, a manual run and a network change. A node out of quarantine starts its
// counters from zero. The shifts stay.
func (b *balancer) rrResetMarks() {
	b.memory.shifts.unmarkAll()
	m := &b.memory
	// Under access: rrCountFail copies the set and stores it back under the same
	// lock, and a swap between the two would bring a lifted quarantine back.
	m.access.Lock()
	defer m.access.Unlock()
	if set := m.quarantine.Swap(nil); set != nil {
		for tag := range *set {
			delete(m.counts, tag)
		}
	}
}

// rrForget drops the counters of tag: the dead verdict. The failures it collected
// while dead say nothing about how it serves connections once it is back.
func (b *balancer) rrForget(tag string) {
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	delete(m.counts, tag)
}

// rrCreditFail counts a confirmed failure of tag; a quarantine refills its slot. A
// node without history is dead: its failures are no measure of the node.
func (s *URLTest) rrCreditFail(tag string) {
	s.rrCreditFailKey(tag, rrKey{})
}

// rrCreditFailKey is rrCreditFail for a failure of the key (no key without
// stickiness): a key counts against a node once (rrSlotCount.keys).
func (s *URLTest) rrCreditFailKey(tag string, key rrKey) {
	if node, loaded := s.outbound.Outbound(tag); !loaded || s.group.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) == nil {
		return
	}
	verdict := s.balancer.rrCountFail(tag, key)
	if !verdict.quarantined {
		return
	}
	s.logger.Info("round_robin: ", tag, " quarantined: ", verdict.fails, " confirmed failures of ", verdict.conns,
		" connections (share ", strconv.FormatFloat(verdict.share, 'f', 3, 64),
		", pool median ", strconv.FormatFloat(verdict.median, 'f', 3, 64), ")")
	go s.rrQuarantineReplace(tag)
}

// rrQuarantineReplace refills the slot of a quarantined node (§3.4) as a check of
// the self-heal: it waits, bounded, for the check in flight and the gap after the
// previous one, and ends through endCheck like any check; a freeze or the group's
// Close aborts it.
func (s *URLTest) rrQuarantineReplace(tag string) {
	g := s.group
	heal := &s.balancer.heal
	deadline := time.Now().Add(rrQuarantineWait)
	for !heal.beginCheck() {
		if time.Now().After(deadline) || !s.balancer.isQuarantined(tag) {
			return
		}
		select {
		case <-g.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer heal.endCheck()
	ctx, cancel := context.WithCancel(g.ctx)
	heal.abort.Store(&cancel)
	defer func() {
		heal.abort.Store(nil)
		cancel()
	}()
	if !s.balancer.isQuarantined(tag) || heal.aborted(ctx) {
		return
	}
	node, loaded := s.outbound.Outbound(tag)
	if !loaded || !s.rrTakeChecking(ctx) {
		return
	}
	defer g.checking.Store(false)
	s.rrReplaceNode(ctx, rrProbe{member: tag, leaf: RealTag(node, N.NetworkTCP), started: time.Now()}, false, true)
}

// --- failures and their verdicts ---------------------------------------------------

// rrFailure is one failure of a connection — a dial that did not open, with its
// retry, or a close without a byte. It is pending on the node it failed on, and on
// the node of its retry when that failed too (rrPend), until each proves alive
// after it (rrTrack.takeAlive) or the dead verdict drops it.
type rrFailure struct {
	s      *URLTest
	key    rrKey
	member string // A, the node the connection failed on

	access        sync.Mutex
	aAlive        bool // A proved alive after the failure
	aShifted      bool
	confirmed     bool // the retry got the first byte: A's failure is confirmed (case 1)
	creditPending bool // confirmed before A proved alive: counted then
	rTag          string
	rFailed       bool // the retry did not open either
	rAlive        bool // the retry's node proved alive after its failure
	rShifted      bool
	shift         int // the key's shift this connection expects: picked with, then its own
	// creditKey is the key of the connection for the count of the node's failures
	// (rrCreditFailKey), kept when key is cleared (no shift).
	creditKey rrKey
	// silence: the failure is silence through a live node (no answer at all): it
	// shifts the key and confirms no failure through the key's passed list.
	silence bool
	// rNoShift: the retry failed between us and its node's server (a proxy leaf):
	// no shift past the retry node.
	rNoShift bool
}

// retryNoShift records that the retry's failure shifts no key.
func (f *rrFailure) retryNoShift() {
	f.access.Lock()
	defer f.access.Unlock()
	f.rNoShift = true
}

func (s *URLTest) rrNewFailure(picked rrPick, member string) *rrFailure {
	key := picked.key
	if picked.detour {
		key = rrKey{}
	}
	return &rrFailure{s: s, key: key, creditKey: picked.key, member: member, shift: picked.shift}
}

// expectedShift is the shift of the key as this connection left it.
func (f *rrFailure) expectedShift() int {
	f.access.Lock()
	defer f.access.Unlock()
	return f.shift
}

// setAlive is the verdict "A alive after the failure".
func (f *rrFailure) setAlive() {
	f.access.Lock()
	defer f.access.Unlock()
	f.aAlive = true
	f.settle()
}

// setRetryAlive is the verdict "the retry's node alive after its failure".
func (f *rrFailure) setRetryAlive() {
	f.access.Lock()
	defer f.access.Unlock()
	f.rAlive = true
	f.settle()
}

// retryFailed records that the retry through tag did not open either.
func (f *rrFailure) retryFailed(tag string) {
	if f == nil {
		return
	}
	f.access.Lock()
	defer f.access.Unlock()
	f.rTag, f.rFailed = tag, true
}

// confirm is the first byte of the retry: A's failure is confirmed (case 1), so the
// key's later first byte does not confirm it again. It reports whether the failure
// counts now: only once A proved alive after it (settle counts it then), never when
// A is found dead or no verdict comes.
func (f *rrFailure) confirm() bool {
	if f == nil {
		return true
	}
	f.access.Lock()
	defer f.access.Unlock()
	f.confirmed = true
	if f.aShifted && f.key.valid {
		f.s.balancer.memory.shifts.markCredited(f.key.table, f.member)
	}
	if f.silence {
		return false // silence confirms no failure, not even through the retry
	}
	if !f.aAlive {
		f.creditPending = true
		return false
	}
	return true
}

// settle shifts the key once the verdicts allow: by one past A when A is alive, by
// one more past the retry node when the retry failed too and its node is alive.
// The caller holds access.
func (f *rrFailure) settle() {
	if f.aAlive && f.creditPending {
		f.creditPending = false
		f.s.rrCreditFailKey(f.member, f.creditKey)
	}
	if !f.key.valid {
		return
	}
	if f.aAlive && !f.aShifted {
		f.aShifted = true
		if shift, shifted := f.s.rrShiftOn(f.key, f.shift, f.member, f.confirmed, f.silence); shifted {
			f.shift = shift
		}
	}
	if !f.aShifted || !f.rFailed || !f.rAlive || f.rShifted || f.rNoShift {
		return
	}
	f.rShifted = true
	if shift, shifted := f.s.rrShiftOn(f.key, f.shift, f.rTag, false, f.silence); shifted {
		f.shift = shift
	}
}

// rrFirstByte is the success of a watched connection: the network is alive, the
// node proved alive, and the failures this success confirms are counted (§3.6.2).
// The proof dates from the start of the connection (its pick), not from the read:
// a byte that waited in a buffer (the kernel, a mux session, QUIC) and was read
// after the node died or the network went away proves nothing about the time
// after the connection started. So only a connection started after a failure
// settles it, as only a probe started after it does; the node's byte mark
// (rrByteSince, the filter of rrHang) keeps the start of the connection too.
func (s *URLTest) rrFirstByte(w *rrConnWatch) {
	b := s.balancer
	if s.group.ctx.Err() != nil {
		return
	}
	b.rrClockLook(time.Now()) // N3 before the rules
	track := w.track
	if track == nil {
		track = b.trackOf(w.member)
	}
	var started int64 // no pick, no proof
	if !w.start.IsZero() {
		started = rrClock(w.start)
		if track != nil {
			track.markProved(started) // rrByteSince; lifts a suspicion of the slot (urltest_rr_track_lx.go)
		}
	}
	if b.heal.frozen() {
		return
	}
	if started != 0 && track != nil && track.pendingN.Load() > 0 {
		s.rrSettleAlive(track, started)
	}
	// A connection that started before the last event of the network confirms
	// nothing.
	if s.rrBeforeEvent(w.start) {
		return
	}
	if w.confirmFor != "" && !w.packet { // UDP stays out of the slot counters
		if w.failure.confirm() {
			var key rrKey
			if w.failure != nil {
				key = w.failure.creditKey
			}
			s.rrCreditFailKey(w.confirmFor, key)
		}
	}
	if w.key.valid {
		for _, tag := range b.memory.shifts.creditable(w.key.table, w.member, w.confirmFor) {
			s.rrCreditFailKey(tag, w.key)
		}
	}
}

// rrSilentClose is a connection that ended without a byte although the client
// sent something (§3.1.2): no retry — the client sent its data to the node already
// — and the failure is pending on the node. It puts the site under suspicion: a
// verdict probe of the node alone starts at once (urltest_rr_track_lx.go); its
// answer, or a first byte through the node, settles the failure, and its silence
// starts the check with a control.
func (s *URLTest) rrSilentClose(w *rrConnWatch) {
	b := s.balancer
	b.rrClockLook(time.Now()) // N3 before the rules
	if s.group.ctx.Err() != nil || b.heal.frozen() || s.rrBeforeEvent(w.start) {
		return
	}
	if w.key.valid && b.memory.shifts.marked(w.key.table) {
		return
	}
	member, loaded := s.outbound.Outbound(w.member)
	if !loaded || !b.inPool(w.member) {
		return
	}
	s.logger.Debug("round_robin: a connection through ", w.member, " closed without a byte from the destination")
	picked := rrPick{key: w.key, shift: w.shift, detour: w.detour}
	if w.failure != nil {
		picked.shift = w.failure.expectedShift() // the retry: the key as its failure left it
	}
	s.rrPend(member, s.rrNewFailure(picked, w.member), false, rrVerdictNow)
}

// lx:end SPEC 138
