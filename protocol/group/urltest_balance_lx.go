package group

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPEC 019 v2 — round_robin load-balancing for the urltest group.
//
// least_test (the default) keeps the legacy "pick lowest-delay node" behaviour and is
// handled by URLTestGroup.Select; this file drives round_robin only.
//
// Model: a FIXED-SIZE pool of slots. Slot indices [0..pool-1] never move and are never
// re-sorted — nodes flow THROUGH slots, a replacement takes the exact slot of the node it
// evicts. Two independent orderings (do not conflate — that was the v1 bug):
//   - slot order (fixed)      → sticky binding: slot[hash(key) % pool]
//   - node rank by last delay → only to find which node to evict (computed on the fly)
//
// The pool is lazily health-checked: we test no more nodes than needed to keep it full of
// live nodes. Selection runs once per new connection (DialContext/ListenPacket).

// slot is one fixed position in the pool. tag is the node currently occupying it.
type slot struct {
	tag   string
	track *rrTrack // lx: SPEC 138 — unfinished exchanges, proofs and suspicion of the node (urltest_rr_track_lx.go)
}

// balancer holds the per-group round_robin state. nil for least_test.
type balancer struct {
	poolSize      int      // target slot count (config pool, before min(pool,nodes))
	poolTolerance uint16   // ms; 0 = first-live-fill, > 0 = top-N-by-delay with eviction threshold
	stickyHash    []string // sticky key components; empty → no stickiness (counter rotation)

	access  sync.Mutex // guards slots
	slots   []slot     // the pool; len == min(poolSize, available nodes), index = fixed slot number
	counter atomic.Uint64

	// onChange, if set, is called after the pool occupancy changes (setSlots). The
	// URLTestGroup wires it to invalidate the SPEC 020 reachable cache, since the
	// active pool just changed. Called WITHOUT the access lock held.
	onChange func()

	// rr, if set, is the self-heal of the group (SPEC 138, rrWire): the pick skips
	// the slots whose node it does not find alive and hands it its looks. Called
	// WITHOUT the access lock held.
	rr       rrOwner
	poolLook atomic.Int64 // rrClock of the next look at the pool

	// The readings of the last comparison of the wall and the monotonic clock (SPEC
	// 138, rrClockLook).
	clockLook   atomic.Int64 // rrClock of the next look: a cheap gate on the hot path
	clockAccess sync.Mutex   // guards the pair of readings of the last look
	clockMono   int64
	clockWall   int64

	// heal is the state of the self-heal after a failed dial (SPEC 138 §3,
	// urltest_rr_heal_lx.go).
	heal rrHeal

	// memory is the key shift table, the slot counters and the quarantine (SPEC 138
	// §4.4–4.5, urltest_rr_shift_lx.go).
	memory rrMemory

	// run is the run of the group in flight (SPEC 019, urltest_rr_run_lx.go).
	run rrRun

	// unverified are the pool nodes the manual test with no answer left without
	// history (SPEC 019, urltest_rr_unverified_lx.go).
	unverified rrUnverified
}

// rrOwner is the self-heal of a round_robin group (SPEC 138), *URLTest: whether the
// node of a slot is alive (slotAlive), the look of the pick at a slot whose tracker
// has work (rrOnPick; now is the rrClock of the pick) and at the whole pool, at most
// once a second (rrPoolLook), a probe of the scheduled run that answered
// (rrRunAnswer), and a jump between the wall and the monotonic clock (rrClockJump):
// a sleep the monotonic clock did not count, or a clock change.
type rrOwner interface {
	slotAlive(tag string) bool
	rrOnPick(ctx context.Context, tag string, track *rrTrack, now int64)
	rrPoolLook(ctx context.Context)
	rrRunAnswer(tag string)
	rrClockJump(drift time.Duration)
}

// warnLegacyTolerance reports whether the legacy urltest `tolerance` option deserves a
// startup warning: round_robin ignores it, but the hint is only useful while the config
// has NOT adopted the round_robin knob — once balancer.pool_tolerance is set, the user
// already knows the replacement and repeating the warning on every start is noise
// (issue #7). pool_tolerance is a uint16, so an explicit 0 is indistinguishable from
// absent and still warns — an explicit 0 means first-live-fill, where tolerance-like
// ranking genuinely does not happen.
func warnLegacyTolerance(b *balancer, options option.URLTestOutboundOptions) bool {
	return b != nil && options.Tolerance != 0 && b.poolTolerance == 0
}

// isFailoverMode reports whether the group runs mode: failover (SPEC 116). Like least_test it
// has no balancer; the flag selects the hold-until-failure policy in urltest_failover_lx.go.
func isFailoverMode(options option.URLTestOutboundOptions) bool {
	return options.Mode == C.URLTestModeFailover
}

// warnFailoverTolerance reports whether `tolerance` deserves a startup warning in failover
// mode: there are no speed-driven switches, so the hysteresis never applies.
func warnFailoverTolerance(failover bool, options option.URLTestOutboundOptions) bool {
	return failover && options.Tolerance != 0
}

// newBalancer builds the balancer from validated options. Returns nil for least_test (and
// empty mode) and failover, so callers branch cheaply on a nil balancer.
func newBalancer(options option.URLTestOutboundOptions) (*balancer, error) {
	mode := options.Mode
	if mode == "" || mode == C.URLTestModeLeastTest || mode == C.URLTestModeFailover {
		return nil, nil
	}
	if mode != C.URLTestModeRoundRobin {
		return nil, E.New("unknown urltest mode: ", mode)
	}
	bo := options.Balancer
	pool := C.DefaultURLTestPool
	var stickyHash []string
	if bo != nil {
		// pool: a Go int with omitempty can't tell "0" from "absent", so 0 means default.
		// Only an explicitly negative value is rejected.
		if bo.Pool < 0 {
			return nil, E.New("urltest balancer.pool must be >= 1")
		}
		if bo.Pool > 0 {
			pool = bo.Pool
		}
		// Omitted (nil) or empty → default. To DISABLE stickiness use the explicit ["none"]
		// sentinel: the config decoder collapses a bare [] to nil (see URLTestStickyNone), so an
		// empty list cannot mean "off". ["none"] → stickiness off (no components).
		if len(bo.StickyHash) == 0 {
			stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
		} else if len(bo.StickyHash) == 1 && bo.StickyHash[0] == C.URLTestStickyNone {
			stickyHash = nil // sticky off → pure counter rotation
		} else {
			stickyHash = bo.StickyHash
		}
	} else {
		// round_robin without balancer → defaults, stickiness on by default.
		stickyHash = []string{C.URLTestStickyProcess, C.URLTestStickyDomain}
	}
	for _, component := range stickyHash {
		switch component {
		case C.URLTestStickyProcess, C.URLTestStickyDomain, C.URLTestStickySourceIP,
			C.URLTestStickyDestIP, C.URLTestStickyDestPort:
		case C.URLTestStickyNone:
			// "none" is only valid as the sole component (handled above); mixed with real
			// components it is ambiguous (disable AND key by something?) — reject.
			return nil, E.New("urltest sticky_hash: \"none\" must be the only component (it disables stickiness)")
		default:
			return nil, E.New("unknown urltest sticky_hash component: ", component)
		}
	}
	var poolTolerance uint16
	if bo != nil {
		// Fail fast on meaningless hysteresis: alive delays cap at ~15000 ms
		// (TCP probe timeout), so a larger tolerance can never evict anyone —
		// that intent is first-live mode (pool_tolerance: 0). Values in the
		// 50536..65535 band also used to overflow the uint16 eviction compare.
		if bo.PoolTolerance > 15000 {
			return nil, E.New("urltest balancer.pool_tolerance is in milliseconds and must be <= 15000 (alive delays cap at the 15s probe timeout); use 0 for first-live mode")
		}
		poolTolerance = bo.PoolTolerance
	}
	return &balancer{poolSize: pool, poolTolerance: poolTolerance, stickyHash: stickyHash}, nil
}

// pickKeyed selects one node for this connection from the current pool and returns
// what the dial of the connection needs from the pick (SPEC 138): the shift-table
// key, computed once per connection, the key's shift and the start of the
// connection. fallback is returned when the pool is empty (start, before the first
// health-check fills it).
// lx: SPEC 138 — a slot whose node failed its last probe (or is in quarantine) is skipped
// while the pool has a live one, and a slot under suspicion (S1–S3) while the pool has a
// slot that is neither; a shifted key takes the slot (hash + shift) % pool, or the next
// live one going round; the pick never changes the slots.
func (b *balancer) pickKeyed(ctx context.Context, network string, destination M.Socksaddr, fallback adapter.Outbound, resolve func(tag string) adapter.Outbound) (adapter.Outbound, rrPick) {
	picked := rrPick{start: time.Now(), packet: network == N.NetworkUDP}
	b.access.Lock()
	slots := b.slots // setSlots publishes a new slice and never mutates a published one
	b.access.Unlock()
	n := len(slots)
	if n == 0 {
		return fallback, picked
	}
	// Reduce in uint64 BEFORE narrowing: int(counter) on a 32-bit platform goes negative
	// past 2^31 and a signed % would produce a negative index (slice panic).
	now := rrClock(picked.start)
	b.rrClockLook(picked.start) // lx: SPEC 138 — N3, at most once a second
	// I9: the pick for a probe of another group or a delay measurement through this
	// one starts nothing here.
	if look := b.poolLook.Load(); b.rr != nil && !rrUnwatched(ctx) && now >= look && b.poolLook.CompareAndSwap(look, now+int64(time.Second)) {
		b.rr.rrPoolLook(ctx) // lx: SPEC 138 — an empty or quarantined slot starts the replacement search
	}
	var idx int
	if len(b.stickyHash) > 0 {
		// slot-hash: fixed slot index from the key; living node in its slot keeps its keys.
		picked.key = b.rrKeyOf(ctx, destination)
		picked.shift = b.memory.shifts.shiftOf(picked.key.table)
		hash := picked.key.sticky
		shift := uint64(picked.shift)
		idx = int((hash%uint64(n) + shift%uint64(n)) % uint64(n))
		if b.rr != nil && !b.rr.slotAlive(slots[idx].tag) {
			if shift > 0 {
				// A shifted key goes on to the next live slot: where its retry went.
				if live, ok := b.firstLiveFrom(slots, idx, now, false); ok {
					idx = live
				}
			} else if live, ok := b.liveSlot(slots, hash/uint64(n), now, false); ok {
				// Keys of a dead slot spread over the live slots by the rest of the hash.
				idx = live
			}
		}
	} else {
		// plain round-robin over the live slots; with all of them live it is counter % n.
		counter := b.rotate(ctx)
		idx = int(counter % uint64(n))
		if b.rr != nil {
			if live, ok := b.liveSlot(slots, counter, now, false); ok {
				idx = live
			}
		}
	}
	// lx: SPEC 138 — the slot found as before is under suspicion (an unfinished
	// connection through it with no proof of its node since, urltest_rr_track_lx.go):
	// the pick takes the nearest slot going round from it that is live and not under
	// suspicion — the distribution of the keys stays as it is; failing that, the slot
	// itself.
	base := idx
	if track := slots[base].track; track != nil && track.suspect(now, b.heal.epoch()) {
		for step := 1; step < n; step++ {
			i := (base + step) % n
			if b.usable(slots[i], now, true) {
				idx = i
				picked.detour = true
				break
			}
		}
	}
	if node := resolve(slots[idx].tag); node != nil {
		// SPEC 138: the slot's tracker goes with the pick; a few atomic loads tell
		// whether it has work for the slow path. The suspect slots the pick went past
		// get their look too, at most once per rrLook (S2, SILENT, D4/D5).
		if track := slots[idx].track; track != nil {
			picked.track = track
			if b.rr != nil && track.attention(now) {
				b.rr.rrOnPick(ctx, slots[idx].tag, track, now)
			}
		}
		if idx != base && b.rr != nil {
			for i := base; i != idx; i = (i + 1) % n {
				if track := slots[i].track; track != nil && track.suspect(now, b.heal.epoch()) && track.attention(now) {
					b.rr.rrOnPick(ctx, slots[i].tag, track, now)
				}
			}
		}
		return node, picked
	}
	return fallback, picked
}

// usable reports whether the pick may take a slot: its node passed its last probe
// and, when strict, the slot is not under suspicion (SPEC 138).
func (b *balancer) usable(s slot, now int64, strict bool) bool {
	if b.rr != nil && !b.rr.slotAlive(s.tag) {
		return false
	}
	return !strict || s.track == nil || !s.track.suspect(now, b.heal.epoch())
}

// liveSlot returns the index of the k-th live slot, k taken modulo the live count. ok is
// false with no live slot (cold start, total outage): the caller keeps its pick.
func (b *balancer) liveSlot(slots []slot, k uint64, now int64, strict bool) (int, bool) {
	var live uint64
	for _, s := range slots {
		if b.usable(s, now, strict) {
			live++
		}
	}
	if live == 0 {
		return 0, false
	}
	k %= live
	for i, s := range slots {
		if b.usable(s, now, strict) {
			if k == 0 {
				return i, true
			}
			k--
		}
	}
	return 0, false // a probe changed the live set between the passes
}

// slotAlive reports whether the node in a slot has URL test history (under its RealTag),
// i.e. its last probe answered, and is not in quarantine. A tag that does not resolve
// counts as dead. lx: SPEC 138.
func (s *URLTest) slotAlive(tag string) bool {
	if s.balancer != nil && s.balancer.isQuarantined(tag) {
		return false
	}
	node, loaded := s.outbound.Outbound(tag)
	return loaded && s.group.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) != nil
}

// poolTags returns the current slot tags (snapshot under lock). Used by Pool()/GetPool.
func (b *balancer) poolTags() []string {
	b.access.Lock()
	defer b.access.Unlock()
	tags := make([]string, len(b.slots))
	for i, s := range b.slots {
		tags[i] = s.tag
	}
	return tags
}

// slotsSnapshot returns the published slot slice. writeSlots publishes a
// new slice and never mutates a published one, so it stays valid without the lock.
func (b *balancer) slotsSnapshot() []slot {
	b.access.Lock()
	defer b.access.Unlock()
	return b.slots
}

// replaceSlot puts node into the slot that holds dead, leaving every other slot as
// is (SPEC 138 §5). It does nothing and returns false when dead no longer holds a
// slot or node already holds one: the pool changed since the caller looked at it.
func (b *balancer) replaceSlot(dead, node string) bool {
	return b.writeSlots(func(old []slot) []slot {
		index := -1
		for i, s := range old {
			if s.tag == node {
				return nil
			}
			if s.tag == dead {
				index = i
			}
		}
		if index < 0 {
			return nil
		}
		next := slices.Clone(old)
		next[index] = slot{tag: node, track: newRRTrack(node)}
		return next
	})
}

// clearSlot empties the slot of tag, leaving every other slot as is (SPEC 138 D1:
// the struck node leaves its slot even with no replacement). It reports whether tag
// held a slot.
func (b *balancer) clearSlot(tag string) bool {
	return b.writeSlots(func(old []slot) []slot {
		index := slotIndex(old, tag)
		if tag == "" || index < 0 {
			return nil
		}
		next := slices.Clone(old)
		next[index] = slot{}
		return next
	})
}

// setSlots replaces the pool atomically. The health-check (urltest_pool_lx.go) computes the
// new occupancy — which tag sits in which slot — and hands the ordered tag list here.
func (b *balancer) setSlots(tags []string) {
	b.writeSlots(func(old []slot) []slot {
		next := make([]slot, len(tags))
		for i, tag := range tags {
			next[i] = slot{tag: tag, track: slotTrack(old, tag)} // lx: SPEC 138 — a node keeps its tracker
		}
		return next
	})
}

// writeSlots is the one writer of the pool: change gets the published slots and returns
// the next ones (nil: no change), which are published as a new slice — a published one is
// never mutated. The nodes that left take their Q1 signs and their counters with them
// (SPEC 138), and the active pool changed (SPEC 020: onChange, called outside the lock so
// the callback never nests under it). It reports whether the pool changed.
func (b *balancer) writeSlots(change func(old []slot) []slot) bool {
	b.access.Lock()
	next := change(b.slots)
	if next == nil {
		b.access.Unlock()
		return false
	}
	departed := departedTracks(b.slots, next)
	b.slots = next
	b.access.Unlock()
	for _, track := range departed {
		track.dropPending() // lx: SPEC 138 — its Q1 signs leave with it
	}
	b.rrSlotsChanged() // lx: SPEC 138 — a node that takes a slot starts its counters from zero
	if b.onChange != nil {
		b.onChange()
	}
	return true
}

// --- sticky key ---------------------------------------------------------------------

// stickyComponent is one component of the binding key, in the text form the slot
// hash covers (stickyHashOf). An absent component contributes "" (SPEC: all-empty →
// key "" → one fixed slot).
func stickyComponent(component string, metadata *adapter.InboundContext, destination M.Socksaddr) string {
	switch component {
	case C.URLTestStickyProcess:
		if metadata == nil || metadata.ProcessInfo == nil {
			return ""
		}
		if len(metadata.ProcessInfo.AndroidPackageNames) > 0 {
			return metadata.ProcessInfo.AndroidPackageNames[0]
		}
		return metadata.ProcessInfo.ProcessPath
	case C.URLTestStickyDomain:
		// The router resolves a domain destination to an IP and overwrites metadata.Destination
		// (route.go: `metadata.Destination = M.Socksaddr{Addr: ...}`) BEFORE the group's
		// DialContext runs — so destination.Fqdn is empty here for domain traffic. The original
		// domain survives in metadata.Domain (set by sniffing / reverse mapping). Read that first;
		// fall back to destination.Fqdn only if it is still a domain (no metadata, direct dial).
		if metadata != nil && metadata.Domain != "" {
			return metadata.Domain
		}
		return destination.Fqdn
	case C.URLTestStickySourceIP:
		if metadata == nil || !metadata.Source.Addr.IsValid() {
			return ""
		}
		return metadata.Source.Addr.String()
	case C.URLTestStickyDestIP:
		if !destination.Addr.IsValid() {
			return ""
		}
		return destination.Addr.String()
	case C.URLTestStickyDestPort:
		if destination.Port == 0 {
			return ""
		}
		return strconv.Itoa(int(destination.Port))
	default:
		return ""
	}
}

// --- pool maintenance (pure planning, no I/O) ---------------------------------------
//
// These compute the NEW slot occupancy from current slots + fresh test results, honouring
// the SPEC invariants: fixed slot count, replace-in-slot, never shrink, dead node keeps its
// slot until a live replacement exists. The caller (urltest.go health-check) runs the actual
// URL tests and applies the result via setSlots.

// candidate is a node and its just-measured delay (0 == not measured / dead).
type candidate struct {
	tag   string
	delay uint16
	alive bool
}

// planFirstLivePool (pool_tolerance == 0) computes slot occupancy WITHOUT ranking by delay:
// every live slot member keeps its exact index, dead/empty slots are refilled from fillOrder
// (live nodes not already pooled, in caller order), and any leftover hole keeps a dead member
// so the pool never shrinks. The lazy scheduled run (balancePoolFirstLive, fillOrder in walk
// order) and the manual-test rebuild (fillOrder by delay) both lay the pool out with it:
// re-ranking would needlessly relocate living nodes and break their sticky bindings.
//
// current = present slot tags (slot order). live = set of tags that tested alive now.
// fillOrder = candidate tags to drop into holes (already filtered to non-pool, in order).
func planFirstLivePool(current []string, live map[string]bool, fillOrder []string, size int) []string {
	slotCount := size
	if len(current) > slotCount {
		slotCount = len(current)
	}
	next := make([]string, slotCount)
	for i, tag := range current {
		if tag != "" && live[tag] {
			next[i] = tag
		}
	}
	placed := make(map[string]bool, len(next))
	for _, tag := range next {
		if tag != "" {
			placed[tag] = true
		}
	}
	// Refill holes with fresh live nodes, writing each into the hole's own index.
	fi := 0
	for i := range next {
		if next[i] != "" {
			continue
		}
		for fi < len(fillOrder) {
			tag := fillOrder[fi]
			fi++
			if tag != "" && live[tag] && !placed[tag] {
				next[i] = tag
				placed[tag] = true
				break
			}
		}
	}
	// Any hole still open: keep a dead member there (never shrink). A dead occupant first keeps
	// the slot it already held; surplus dead members fall into leftover holes.
	for i, tag := range current {
		if i < len(next) && next[i] == "" && tag != "" && !live[tag] && !placed[tag] {
			next[i] = tag
			placed[tag] = true
		}
	}
	for i := range next {
		if next[i] != "" {
			continue
		}
		for _, tag := range current {
			if tag != "" && !live[tag] && !placed[tag] {
				next[i] = tag
				placed[tag] = true
				break
			}
		}
	}
	return next
}

// planTolerantPool (pool_tolerance > 0): pick the top-`size` live candidates by delay, but
// keep a node in its current slot when it survives (replace-in-slot, no needless churn). A
// living slot is only displaced when some out-of-pool node is faster than it by > tolerance.
//
// current = present slot tags (in slot order). results = delay for every node that was
// tested (alive ones); size = min(poolSize, len(all nodes)).
func planTolerantPool(current []string, results map[string]candidate, size int, tolerance uint16) []string {
	// Rank all alive candidates by delay ascending (tag breaks ties — deterministic).
	alive := make([]candidate, 0, len(results))
	for _, c := range results {
		if c.alive {
			alive = append(alive, c)
		}
	}
	sortCandidatesByDelay(alive)

	next := make([]string, len(current))
	copy(next, current)
	// Grow to size if we have spare slots and alive nodes not yet placed.
	inPool := make(map[string]bool, len(next))
	for _, tag := range next {
		if tag != "" {
			inPool[tag] = true
		}
	}
	for len(next) < size {
		next = append(next, "")
	}
	// Fill empty slots first with the fastest unused alive nodes.
	for i := range next {
		if next[i] != "" {
			continue
		}
		for _, c := range alive {
			if !inPool[c.tag] {
				next[i] = c.tag
				inPool[c.tag] = true
				break
			}
		}
	}
	// Eviction: for each slot, if a faster-than-by-tolerance unused alive node exists, swap it
	// IN PLACE. We do NOT return the evicted occupant to the candidate set: re-inserting it at a
	// later slot would relocate a node across slots and move every sticky key bound to it. A
	// surviving occupant therefore always keeps its slot index; only the evicted-and-replaced
	// slot changes its node (the SPEC replace-in-slot invariant).
	for i := range next {
		occupant := next[i]
		occDelay, occAlive := slotDelay(occupant, results)
		for _, c := range alive {
			if inPool[c.tag] {
				continue
			}
			// Replace if the slot is dead, or the candidate beats it by > tolerance.
			// Compare in uint32: c.delay+tolerance wraps uint16 for a large configured
			// pool_tolerance and would invert the hysteresis into aggressive churn.
			if !occAlive || (occAlive && uint32(c.delay)+uint32(tolerance) < uint32(occDelay)) {
				next[i] = c.tag
				inPool[c.tag] = true
				break
			}
		}
	}
	return next
}

func slotDelay(tag string, results map[string]candidate) (uint16, bool) {
	if tag == "" {
		return 0, false
	}
	if c, ok := results[tag]; ok {
		return c.delay, c.alive
	}
	return 0, false
}

func sortCandidatesByDelay(cs []candidate) {
	// insertion sort — pool/result sets are tiny; avoids pulling sort.Slice closure cost.
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0; j-- {
			a, b := cs[j-1], cs[j]
			if a.delay < b.delay || (a.delay == b.delay && a.tag <= b.tag) {
				break
			}
			cs[j-1], cs[j] = cs[j], cs[j-1]
		}
	}
}

// ActiveTags returns the tags this urltest group is CURRENTLY routing through —
// the whole rotation pool for a round_robin (balanced) group, or the single
// current node for a legacy least_test group. Used by the SPEC 020 reachability
// walk: every node a balanced group could dial right now is reachable (and must
// not be idle-suspended), not just Now(). Returns nil for an empty/cold group.
func (s *URLTest) ActiveTags() []string {
	if s.balancer != nil {
		if tags := s.balancer.poolTags(); len(tags) > 0 {
			return tags
		}
		// Cold start: pool not yet filled — fall back to the single current pick.
	}
	if now := adapter.GroupNow(s); now != "" {
		return []string{now}
	}
	return nil
}

// poolReferences is what a round_robin group holds in use: every pool slot is
// dialed, not one selected node, so every slot counts as referenced. Without it
// upstream's reference manager treats the rest of the pool as unreferenced and
// closes their idle connections after each use. nil for least_test.
// lx: SPEC 109.
func (s *URLTest) poolReferences() []string {
	if s.balancer == nil {
		return nil
	}
	var references []string
	for _, tag := range s.balancer.poolTags() {
		if tag != "" {
			references = append(references, tag)
		}
	}
	return references
}
