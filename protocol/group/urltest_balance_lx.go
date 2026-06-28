package group

import (
	"context"
	"hash/fnv"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
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
	tag string
}

// balancer holds the per-group round_robin state. nil for least_test.
type balancer struct {
	poolSize      int      // target slot count (config pool, before min(pool,nodes))
	poolTolerance uint16   // ms; 0 = first-live-fill, > 0 = top-N-by-delay with eviction threshold
	stickyHash    []string // sticky key components; empty → no stickiness (counter rotation)

	access  sync.Mutex // guards slots
	slots   []slot     // the pool; len == min(poolSize, available nodes), index = fixed slot number
	counter atomic.Uint64
}

// newBalancer builds the balancer from validated options. Returns nil for least_test (and
// empty mode), so callers branch cheaply on a nil balancer.
func newBalancer(options option.URLTestOutboundOptions) (*balancer, error) {
	mode := options.Mode
	if mode == "" || mode == C.URLTestModeLeastTest {
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
		poolTolerance = bo.PoolTolerance
	}
	return &balancer{poolSize: pool, poolTolerance: poolTolerance, stickyHash: stickyHash}, nil
}

// pick selects one node for this connection from the current pool. fallback is returned
// when the pool is empty (start, before the first health-check fills it).
func (b *balancer) pick(ctx context.Context, destination M.Socksaddr, fallback adapter.Outbound, resolve func(tag string) adapter.Outbound) adapter.Outbound {
	b.access.Lock()
	n := len(b.slots)
	if n == 0 {
		b.access.Unlock()
		return fallback
	}
	var tag string
	if len(b.stickyHash) > 0 {
		// slot-hash: fixed slot index from the key; living node in its slot keeps its keys.
		idx := int(hashKey(b.stickyKey(ctx, destination)) % uint64(n))
		tag = b.slots[idx].tag
	} else {
		// plain round-robin over the fixed slots.
		idx := int(b.counter.Add(1)-1) % n
		tag = b.slots[idx].tag
	}
	b.access.Unlock()
	if node := resolve(tag); node != nil {
		return node
	}
	return fallback
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

// setSlots replaces the pool atomically. The health-check (urltest.go) computes the new
// occupancy — which tag sits in which slot — and hands the ordered tag list here.
func (b *balancer) setSlots(tags []string) {
	b.access.Lock()
	defer b.access.Unlock()
	b.slots = make([]slot, len(tags))
	for i, tag := range tags {
		b.slots[i] = slot{tag: tag}
	}
}

// --- sticky key ---------------------------------------------------------------------

// stickyKey builds the binding key from the configured components, in order. An absent
// component contributes "" (SPEC: all-empty → key "" → one fixed slot).
func (b *balancer) stickyKey(ctx context.Context, destination M.Socksaddr) string {
	metadata := adapter.ContextFrom(ctx)
	var buf []byte
	for i, component := range b.stickyHash {
		if i > 0 {
			buf = append(buf, 0) // NUL separator
		}
		buf = append(buf, stickyComponent(component, metadata, destination)...)
	}
	return string(buf)
}

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

func hashKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
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
// so the pool never shrinks. This is the replace-in-slot twin of balancePoolFirstLive, used by
// the manual-test rebuild path where re-ranking would needlessly relocate living nodes and
// break their sticky bindings.
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
			if !occAlive || (occAlive && c.delay+tolerance < occDelay) {
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
