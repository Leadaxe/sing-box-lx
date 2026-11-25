package group

// lx:begin SPEC 138 round_robin: the key shift, the K2 memory, the slot counters
//
// The rules are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/ALGORITHM.md: the
// key shift K1–K5 (§4.4), the slot counters and the quarantine Q1–Q4 (§4.5).
//
// How it is kept, per group (rrMemory in the balancer):
//   - the shift table: rrShiftTableSize keys, the oldest dropped first, read by the
//     pick under a read lock behind an atomic size; the key of the table is hashed
//     from the sticky key and the destination without building strings (rrKeyOf);
//   - the K2 memory: a ring of rrK2Size records with an index; a record is the first
//     soft sign and the tracker it was taken on (the evidence the group still holds);
//   - the counters of the slots and strikes(n) under one lock, the quarantine an
//     atomic copy-on-write set.

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
	// the next one instead; a sign there shifts no key and writes no K2 memory.
	detour bool
	packet bool // a UDP exchange: its dial waits in pwaits (V9)
	// inner: an outer round_robin group opens this connection (rrInnerDial); the
	// retry is that group's (§5).
	inner bool
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// fnvString continues an FNV-1a hash over s.
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

// stickyHashOf is the slot hash of a connection: FNV-1a, 64 bit, over the sticky
// components joined by NUL, each in its text form (stickyComponent) — computed
// without building the key. The layout of the sticky slots rests on it.
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

type rrShiftEntry struct {
	shift  int
	marked bool         // K5: an address problem
	run    atomic.Int32 // K5: shifts in a row with no ANSWER to the key
	// The current shift made by K2 from k2From to k2To (k2 set), and whether it
	// rested on a SILENT: a cancelled SILENT takes it back (§2).
	k2       bool
	k2Silent bool
	k2From   int
	k2To     int
}

// rrShiftCause is the rule that shifts a key.
type rrShiftCause uint8

const (
	rrShiftK1       rrShiftCause = iota // an explicit REFUSE_DST
	rrShiftK2                           // the second soft sign
	rrShiftK2Silent                     // the second soft sign, one of the two a SILENT
)

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

// marked reports whether key is an address problem (K5).
func (t *rrShiftTable) marked(key uint64) bool {
	if t.size.Load() == 0 {
		return false
	}
	t.access.RLock()
	defer t.access.RUnlock()
	e := t.entries[key]
	return e != nil && e.marked
}

// answered is an ANSWER to key: K5 counts the circle anew.
func (t *rrShiftTable) answered(key uint64) {
	if t.size.Load() == 0 {
		return
	}
	t.access.RLock()
	defer t.access.RUnlock()
	if e := t.entries[key]; e != nil && e.run.Load() != 0 {
		e.run.Store(0)
	}
}

// unmarkAll lifts every K5 mark.
func (t *rrShiftTable) unmarkAll() {
	t.access.Lock()
	defer t.access.Unlock()
	for _, e := range t.entries {
		e.marked = false
	}
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

// rrShiftOn shifts key past node (K1, K2 by cause): the slot after the node becomes
// the slot of the key. expected is the shift the connection was picked with: another
// shift since then means a parallel connection moved it already, and nothing
// changes. |P| shifts in a row with no ANSWER to the key mark it (K5). It returns
// the shift of the key and whether this call changed it.
func (s *URLTest) rrShiftOn(key rrKey, expected int, node string, cause rrShiftCause) (int, bool) {
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
	table := &b.memory.shifts
	table.access.Lock()
	e := table.entry(key.table)
	if e.marked || e.shift != expected {
		shift := e.shift
		table.access.Unlock()
		return shift, false
	}
	current := (int(key.sticky%uint64(n)) + e.shift) % n
	e.shift += (failed-current+n)%n + 1
	e.k2, e.k2Silent, e.k2From, e.k2To = cause != rrShiftK1, cause == rrShiftK2Silent, expected, e.shift
	marked := e.run.Add(1) >= int32(n)
	if marked {
		e.marked, e.shift, e.k2 = true, 0, false
		e.run.Store(0)
	}
	shift := e.shift
	table.access.Unlock()
	if marked {
		s.logger.Info("round_robin: ", key.label(b.stickyHash), " is unreachable through every node of the pool; marked as an address problem, back to its own slot")
	} else {
		s.logger.Debug("round_robin: ", key.label(b.stickyHash), " is unreachable through ", node, "; key shifted by ", shift)
	}
	return shift, true
}

// rrShiftBack takes back the shift K2 made from the shift from, while it is still
// the shift of the key: a SILENT it rested on got its ANSWER (§2, silent), or an
// ANSWER to the key since a1 came in while the shift was made (K2).
func (s *URLTest) rrShiftBack(key rrKey, from int, silent bool) {
	table := &s.balancer.memory.shifts
	if table.size.Load() == 0 {
		return
	}
	table.access.Lock()
	defer table.access.Unlock()
	if e := table.entries[key.table]; e != nil && e.k2 && !e.marked && e.k2From == from && e.shift == e.k2To && (e.k2Silent || !silent) {
		e.shift, e.k2 = from, false
		if e.run.Load() > 0 {
			e.run.Add(-1)
		}
	}
}

// --- the K2 memory ----------------------------------------------------------------

// rrK2Size bounds the K2 memory of a group; the oldest record goes first (I7).
const rrK2Size = 256

type rrK2Key struct {
	key  uint64 // the table key
	node string
}

// rrK2Record is the first soft sign of a key on a node: the shift it was picked
// with, its start, its outcome, the tracker of the node it was taken on and whether
// it is a SILENT (an ANSWER may still cancel it).
type rrK2Record struct {
	k      rrK2Key
	s      int
	a1     int64
	b1     int64
	track  *rrTrack
	silent bool
	inUse  bool
}

type rrK2Table struct {
	access sync.Mutex
	index  map[rrK2Key]int
	ring   [rrK2Size]rrK2Record
	next   int
	size   atomic.Int32
}

func (m *rrK2Table) drop(i int) {
	delete(m.index, m.ring[i].k)
	m.ring[i] = rrK2Record{}
	m.size.Add(-1)
}

// sign takes a soft sign of k picked at shift s, started at a, ended at b, on the
// node of t. It reports K2 with the first sign when the record of k qualifies: made
// after E on the same tracker, at the same shift, the second sign started after its
// outcome, an ANSWER via the node between the two starts (the front of t), and no
// ANSWER to the key via the node that started at or after a1 (its cell). The front
// is read before the cell: an ANSWER writes its cell before its proof, so a proof of
// the key itself seen in the front shows in the cell too. Otherwise the sign
// becomes the record (K4); a sign that started before the outcome of the record is
// the same attempt and leaves it.
func (m *rrK2Table) sign(k rrK2Key, s int, a, b, epoch int64, t *rrTrack, silent bool) (rrK2Record, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	record := rrK2Record{k: k, s: s, a1: a, b1: b, track: t, silent: silent, inUse: true}
	if i, found := m.index[k]; found {
		first := m.ring[i]
		if first.a1 >= epoch && first.s == s && first.track == t {
			if a <= first.b1 {
				return rrK2Record{}, false // K2: a parallel connection of the key is the same attempt
			}
			if t.live(first.a1, a) && !t.keyAnsweredSince(k.key, first.a1) {
				m.drop(i)
				return first, true
			}
		}
		m.ring[i] = record // K4: the sign is the record now
		return rrK2Record{}, false
	}
	if m.index == nil {
		m.index = make(map[rrK2Key]int)
	}
	i := m.next
	m.next = (m.next + 1) % rrK2Size
	if m.ring[i].inUse {
		delete(m.index, m.ring[i].k)
	} else {
		m.size.Add(1)
	}
	m.ring[i] = record
	m.index[k] = i
	return rrK2Record{}, false
}

// keep erases the records of every node keep reports false for.
func (m *rrK2Table) keep(keep func(node string) bool) {
	if m.size.Load() == 0 {
		return
	}
	m.access.Lock()
	defer m.access.Unlock()
	for i := range m.ring {
		if m.ring[i].inUse && !keep(m.ring[i].k.node) {
			m.drop(i)
		}
	}
}

// forget erases the records of node: D1, Q2.
func (m *rrK2Table) forget(node string) {
	m.keep(func(tag string) bool { return tag != node })
}

// rrSoftSign is a soft sign of the key on node (K2, K4): FAIL_NODE, an implicit
// REFUSE_DST or SILENT (silent), picked at shift, started at a, ended at at.
// eligible is false for a sign on the slot of a retry or a detour and for UDP.
func (s *URLTest) rrSoftSign(key rrKey, shift int, node string, a, at int64, silent bool, eligible bool) {
	b := s.balancer
	if !eligible || !key.valid || b.memory.shifts.marked(key.table) {
		return
	}
	t := b.trackOf(node)
	if t == nil {
		return
	}
	first, fired := b.memory.k2.sign(rrK2Key{key.table, node}, shift, a, at, b.heal.epoch(), t, silent)
	if !fired {
		return // K4: the first sign is memory only
	}
	cause := rrShiftK2
	if first.silent || silent {
		cause = rrShiftK2Silent
	}
	if _, shifted := s.rrShiftOn(key, shift, node, cause); shifted && t.keyAnsweredSince(key.table, first.a1) {
		// K2: an ANSWER to the key since a1 — a SILENT of the two getting its answer,
		// or another connection — came in while the shift was made; it found no
		// shift to take back, so the shift goes back here.
		s.rrShiftBack(key, shift, false)
	}
}

// --- slot counters and quarantine ----------------------------------------------

const (
	// rrShiftTableSize bounds the shift table; the oldest key goes first.
	rrShiftTableSize = 1024
	// rrCountHalveAt halves both slot counters when the connections reach it (Q3).
	rrCountHalveAt = 256
	// rrQuarantineMinFails, rrQuarantineMinShare, rrQuarantineMinConns are the
	// floors of Q2.
	rrQuarantineMinFails = 3
	rrQuarantineMinShare = 0.05
	rrQuarantineMinConns = 8
	// rrCountKeys bounds the ring of keys of a slot counter (once per key).
	rrCountKeys = 16
)

type rrSlotCount struct {
	conns uint32
	fails uint32
	keys  [rrCountKeys]uint64 // the table keys counted already
	next  int
}

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

// rrMemory is the key state of a round_robin group; it lives in the balancer.
type rrMemory struct {
	shifts     rrShiftTable
	k2         rrK2Table
	access     sync.Mutex // guards counts
	counts     map[string]*rrSlotCount
	quarantine atomic.Pointer[map[string]bool]
	// strikes is strikes(n) of Q4: the D1 of the node since the scheduled run.
	strikes map[string]int
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

// share returns the failure share of tag and whether it counts (Q2: at least
// rrQuarantineMinConns connections). The caller holds access.
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

// rrCountConn counts a TCP connection of the pool member tag that started at
// start (conns of Q1/Q2), unless it began before E. The membership is checked
// under access, as rrSlotsChanged drops the counters of a departed node.
func (b *balancer) rrCountConn(tag string, start time.Time) {
	if !b.heal.counts(start) {
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
	if c.conns >= rrCountHalveAt { // Q3
		c.conns /= 2
		c.fails /= 2
	}
}

// rrQuarantineVerdict is the outcome of one counted sign.
type rrQuarantineVerdict struct {
	counted     bool
	quarantined bool
	fails       uint32
	conns       uint32
	share       float64
	median      float64
}

// rrCountFail is Q1's fails(n) += 1, once per key, and Q2 on the result.
func (b *balancer) rrCountFail(tag string, key rrKey) rrQuarantineVerdict {
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	slots := b.slotsSnapshot()
	if slotIndex(slots, tag) < 0 {
		return rrQuarantineVerdict{}
	}
	c := m.count(tag)
	if key.valid && c.creditedKey(key.table) {
		return rrQuarantineVerdict{}
	}
	c.fails++
	share, counts := m.share(tag)
	verdict := rrQuarantineVerdict{counted: true, fails: c.fails, conns: c.conns, share: share}
	if !counts || c.fails < rrQuarantineMinFails || share < rrQuarantineMinShare || b.isQuarantined(tag) {
		return verdict
	}
	others := make([]float64, 0, len(slots))
	for _, entry := range slots {
		if entry.tag == tag || entry.tag == "" || b.isQuarantined(entry.tag) || b.rr != nil && !b.rr.slotAlive(entry.tag) {
			continue // Q2: the median over P, the nodes of the pool: no quarantined node, no node without history (§4.2)
		}
		if other, counts := m.share(entry.tag); counts {
			others = append(others, other)
		}
	}
	if len(others) == 0 {
		return verdict
	}
	verdict.median = rrMedian(others)
	if verdict.share <= 2*verdict.median {
		return verdict
	}
	m.quarantineLocked(tag)
	verdict.quarantined = true
	return verdict
}

// quarantineLocked puts tag in quarantine (Q2, Q4); the caller holds access.
func (m *rrMemory) quarantineLocked(tag string) {
	next := make(map[string]bool)
	if set := m.quarantine.Load(); set != nil {
		for quarantined := range *set {
			next[quarantined] = true
		}
	}
	next[tag] = true
	m.quarantine.Store(&next)
	delete(m.counts, tag) // the counters of the node start from zero
}

// rrScheduledRun is what the scheduled run does to the memory at its first answer
// (rrRunAnswered): the marks and the quarantine lift (rrResetMarks) and strikes(n)
// of Q4 start from zero. It returns the nodes lifted from quarantine.
func (b *balancer) rrScheduledRun() []string {
	lifted := b.rrResetMarks()
	m := &b.memory
	m.access.Lock()
	m.strikes = nil
	m.access.Unlock()
	return lifted
}

// rrUncountFail takes back the count of a SILENT that got its ANSWER (Q1).
func (b *balancer) rrUncountFail(tag string, key rrKey) {
	m := &b.memory
	m.access.Lock()
	defer m.access.Unlock()
	c := m.counts[tag]
	if c == nil {
		return
	}
	if c.fails > 0 {
		c.fails--
	}
	for i := range c.keys {
		if key.valid && c.keys[i] == key.table {
			c.keys[i] = 0
		}
	}
}

func rrMedian(values []float64) float64 {
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

// rrSlotsChanged drops the counters (Q3) and the K2 records of the nodes that left
// the pool. Called after the slots change, without access held.
func (b *balancer) rrSlotsChanged() {
	slots := b.slotsSnapshot()
	m := &b.memory
	m.access.Lock()
	for tag := range m.counts {
		if slotIndex(slots, tag) < 0 {
			delete(m.counts, tag)
		}
	}
	m.access.Unlock()
	m.k2.keep(func(node string) bool { return slotIndex(slots, node) >= 0 })
}

// rrResetMarks is the scheduled run, a manual run and a network change: the K5
// marks and the quarantine lift (a node out of quarantine starts its counters from
// zero), the K2 memory and the suspicions go (S3). The shifts stay. It returns the
// nodes lifted from quarantine.
func (b *balancer) rrResetMarks() []string {
	b.memory.shifts.unmarkAll()
	b.memory.k2.keep(func(string) bool { return false })
	for _, entry := range b.slotsSnapshot() {
		if entry.track != nil {
			entry.track.clearSuspicion()
		}
	}
	m := &b.memory
	// Under access: rrCountFail copies the set and stores it back under the same
	// lock, and a swap between the two would bring a lifted quarantine back.
	m.access.Lock()
	defer m.access.Unlock()
	var lifted []string
	if set := m.quarantine.Swap(nil); set != nil {
		for tag := range *set {
			delete(m.counts, tag)
			lifted = append(lifted, tag)
		}
	}
	return lifted
}

// rrStrikeOut is what D1 does to the memory of the node tag: its counters go (Q3),
// its K2 records are erased, strikes(n) += 1, and the second D1 since the scheduled
// run is the quarantine of Q4 when the rest of the pool is not empty (others). It
// reports the quarantine.
func (b *balancer) rrStrikeOut(tag string, others bool) bool {
	m := &b.memory
	m.access.Lock()
	delete(m.counts, tag)
	if m.strikes == nil {
		m.strikes = make(map[string]int)
	}
	m.strikes[tag]++
	quarantined := m.strikes[tag] >= 2 && others
	if quarantined {
		m.quarantineLocked(tag) // the node is no candidate until the scheduled run; its exits are those of Q2
	}
	m.access.Unlock()
	m.k2.forget(tag)
	b.rrRunStrike(tag) // SPEC 019 — the scheduled run in flight puts it into no slot
	return quarantined
}

// rrCreditFail counts a sign of tag by Q1 and reports whether it counted. A node
// without history was struck out (D1): its signs do not count. A quarantine (Q2)
// erases the node's K2 records and asks the check for the replacement (§5).
func (s *URLTest) rrCreditFail(tag string, key rrKey) bool {
	node, loaded := s.outbound.Outbound(tag)
	if !loaded || s.balancer.heal.frozen() || s.group.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) == nil {
		return false
	}
	verdict := s.balancer.rrCountFail(tag, key)
	if verdict.quarantined {
		s.logger.Info("round_robin: ", tag, " quarantined: ", verdict.fails, " failures of ", verdict.conns,
			" connections (share ", strconv.FormatFloat(verdict.share, 'f', 3, 64),
			", pool median ", strconv.FormatFloat(verdict.median, 'f', 3, 64), ")")
		s.balancer.memory.k2.forget(tag)
		s.rrStartSearch() // §5: the slot of a quarantined node gets a replacement

	}
	return verdict.counted
}

// --- Q1 signs ---------------------------------------------------------------------

const (
	rrSignProof uint32 = 1 << iota // an ANSWER via the node that started after the sign
	rrSignRetry                    // the retry of the same connection answered
	rrSignDone
)

// rrSign is a sign of a node waiting for what Q1 needs to count it: need holds the
// bits that must come in (FAIL_NODE: the proof and the retry; an implicit
// REFUSE_DST: the proof; K1: the retry).
type rrSign struct {
	s     *URLTest
	node  string
	key   rrKey
	b     int64 // rrClock of the outcome
	need  uint32
	state atomic.Uint32
}

func (x *rrSign) add(bit uint32) {
	for {
		old := x.state.Load()
		if old&rrSignDone != 0 {
			return
		}
		next := old | bit
		if next&x.need == x.need {
			next |= rrSignDone
		}
		if x.state.CompareAndSwap(old, next) {
			if next&rrSignDone != 0 {
				x.s.rrCreditFail(x.node, x.key)
			}
			return
		}
	}
}

// --- outcomes of a watched connection ----------------------------------------------

// rrFirstByte is the ANSWER of a watched connection (A1): a proof via its node from
// its start to now, the retry's part of a Q1 sign, the K2 record of the key on the
// node erased, the K5 circle restarted, and the SILENT of this connection taken
// back. c is the wrapper; nil for a direct leaf and UDP.
func (s *URLTest) rrFirstByte(w *rrConnWatch, c *rrFirstByteConn) {
	b := s.balancer
	if s.group.ctx.Err() != nil {
		return
	}
	now := time.Now()
	b.rrClockLook(now) // N3 before the rules
	if w.start.IsZero() || !b.heal.counts(w.start) {
		return // N0, I4
	}
	t := w.track
	if t == nil {
		t = b.trackOf(w.member)
	}
	if t != nil {
		if w.key.valid {
			t.keyAnswered(w.key.table, rrClock(w.start)) // K2: before the proof (rrK2Table.sign)
		}
		s.rrProof(t, rrClock(w.start), rrClock(now))
	}
	if w.sign != nil {
		w.sign.add(rrSignRetry)
	}
	if w.key.valid {
		b.memory.shifts.answered(w.key.table) // K5: the circle starts anew
	}
	if b.heal.grantExtra(now, w.start) {
		s.rrExtraRound() // W5: one round at once in a pause above the base
	}
	if c == nil || c.state.Load()&rrStateSilent == 0 {
		return
	}
	c.settle()                          // Q1: the count of its SILENT goes back
	s.rrShiftBack(w.key, w.shift, true) // K2: the shift that rested on it goes back
}

// rrSilentClose is an implicit REFUSE_DST (§2): the connection ended without a byte
// although the client sent and did not close its side. No retry — the client sent
// its data to the node already. K2/K4 take it; Q1 counts it once an ANSWER via the
// node after it comes — a close later than H after the send only with Live(X; {n}).
func (s *URLTest) rrSilentClose(c *rrFirstByteConn) {
	w := c.watch
	b := s.balancer
	now := time.Now()
	b.rrClockLook(now) // N3 before the rules
	if s.group.ctx.Err() != nil || !b.heal.counts(w.start) || w.track == nil || !b.inPool(w.member) {
		return
	}
	s.logger.Debug("round_robin: a connection through ", w.member, " closed without a byte from the destination")
	at, wrote := rrClock(now), c.wroteAt.Load()
	s.rrSoftSign(w.key, w.shift, w.member, rrClock(w.start), at, false, !w.retry && !w.detour)
	if at-wrote < int64(rrHangAfter) || w.track.live(wrote, rrHalf(wrote, at)) {
		w.track.addPending(&rrSign{s: s, node: w.member, key: w.key, b: at, need: rrSignProof})
	}
}

// rrSilent is SILENT given to the connection c at a look (rrInspect): Q1 counts it
// at once (V1), K2/K4 take it.
func (s *URLTest) rrSilent(c *rrFirstByteConn) {
	w := c.watch
	s.logger.Debug("round_robin: a connection through ", w.member, " has had no answer from ", w.key.label(s.balancer.stickyHash), " for ", rrProbeTimeout(), " while the node answers")
	if s.rrCreditFail(w.member, w.key) {
		c.set(rrStateCounted)
		c.settle() // Q1: the ANSWER may have come since SILENT was given
	}
	s.rrSoftSign(w.key, w.shift, w.member, rrClock(w.start), rrClock(time.Now()), true, !w.retry && !w.detour)
}

// lx:end SPEC 138
