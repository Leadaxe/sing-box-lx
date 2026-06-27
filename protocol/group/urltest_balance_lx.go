package group

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

// lx: SPEC 019 — load-balancing for the urltest group.
//
// least_test (the default) keeps the legacy "pick lowest-delay node" behaviour and
// is handled entirely by URLTestGroup.Select; this file only drives round_robin (and,
// in phase 2, least_connection). The selection runs once per connection (DialContext /
// ListenPacket) over the set of *live* nodes — those with a fresh URL-test history that
// support the network — so the existing health ticker is the single source of liveness.

// balancer holds the per-group balancing state. It is nil when mode == least_test.
type balancer struct {
	mode    string
	sticky  *stickyTable // nil when no sticky.hash configured
	access  sync.Mutex
	counter uint64 // round-robin cursor (guarded by access)
}

// newBalancer builds the balancer from validated options. Returns nil for least_test
// (and for an empty mode), so callers can cheaply branch on a nil balancer.
func newBalancer(options option.URLTestOutboundOptions) (*balancer, error) {
	mode := options.Mode
	if mode == "" || mode == C.URLTestModeLeastTest {
		return nil, nil
	}
	switch mode {
	case C.URLTestModeRoundRobin:
	case C.URLTestModeLeastConnection:
		return nil, E.New("urltest mode ", C.URLTestModeLeastConnection, " is not implemented yet (SPEC 019 phase 2)")
	default:
		return nil, E.New("unknown urltest mode: ", mode)
	}
	b := &balancer{mode: mode}
	if options.Sticky != nil && len(options.Sticky.Hash) > 0 {
		sticky, err := newStickyTable(options.Sticky)
		if err != nil {
			return nil, err
		}
		b.sticky = sticky
	}
	return b, nil
}

// pick selects one live outbound for this connection. live must be the tag-sorted live
// set for the network; fallback is the node returned when live is empty (outbounds[0]).
func (b *balancer) pick(ctx context.Context, destination M.Socksaddr, live []adapter.Outbound, fallback adapter.Outbound) adapter.Outbound {
	if len(live) == 0 {
		return fallback
	}
	if len(live) == 1 {
		return live[0]
	}
	// Sticky binding takes precedence: a bound key always returns its node (re-pinned
	// only when that node drops out of the live set).
	if b.sticky != nil {
		key := b.sticky.key(ctx, destination)
		if node := b.sticky.lookup(key, live, b.rotate); node != nil {
			return node
		}
	}
	return b.rotate(live)
}

// rotate is the non-sticky selector for the configured mode. round_robin advances a
// shared cursor across the live set; the cursor is monotonic so the start point is
// stable (the live set is sorted by tag, and Select already begins from the best node).
func (b *balancer) rotate(live []adapter.Outbound) adapter.Outbound {
	switch b.mode {
	case C.URLTestModeRoundRobin:
		b.access.Lock()
		idx := b.counter % uint64(len(live))
		b.counter++
		b.access.Unlock()
		return live[idx]
	default:
		// least_connection is rejected at construction; nothing else reaches here.
		return live[0]
	}
}

func (b *balancer) close() {
	if b.sticky != nil {
		b.sticky.close()
	}
}

// --- sticky key ---------------------------------------------------------------------

// stickyKeyBuilder extracts the configured key components in order. An absent component
// contributes "" (per SPEC 019: empty components are kept, all-empty yields key "").
type stickyKeyBuilder struct {
	components []string
}

func (k stickyKeyBuilder) build(ctx context.Context, destination M.Socksaddr) string {
	metadata := adapter.ContextFrom(ctx)
	var b []byte
	for i, component := range k.components {
		if i > 0 {
			b = append(b, 0) // NUL separator — components can't collide across boundaries
		}
		b = append(b, stickyComponent(component, metadata, destination)...)
	}
	return string(b)
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

// --- sticky table -------------------------------------------------------------------

type stickyEntry struct {
	tag      string
	lastTime time.Time
}

// stickyTable binds keys to nodes. mode jumphash is stateless (table/ticker unused);
// mode ttlmap keeps a key->{tag,lastTime} map with lazy + ticker eviction and an LRU cap.
type stickyTable struct {
	builder stickyKeyBuilder
	mode    string
	timeout time.Duration
	cap     int

	access  sync.Mutex
	entries map[string]*stickyEntry
	ticker  *time.Ticker
	done    chan struct{}
}

func newStickyTable(options *option.URLTestStickyOptions) (*stickyTable, error) {
	mode := options.Mode
	if mode == "" {
		mode = C.URLTestStickyJumpHash
	}
	if mode != C.URLTestStickyJumpHash && mode != C.URLTestStickyTTLMap {
		return nil, E.New("unknown urltest sticky mode: ", mode)
	}
	for _, component := range options.Hash {
		switch component {
		case C.URLTestStickyProcess, C.URLTestStickyDomain, C.URLTestStickySourceIP,
			C.URLTestStickyDestIP, C.URLTestStickyDestPort:
		default:
			return nil, E.New("unknown urltest sticky hash component: ", component)
		}
	}
	if options.Cap < 0 {
		return nil, E.New("urltest sticky cap must be >= 0")
	}
	t := &stickyTable{
		builder: stickyKeyBuilder{components: options.Hash},
		mode:    mode,
	}
	if mode == C.URLTestStickyTTLMap {
		t.timeout = time.Duration(options.Timeout)
		if t.timeout <= 0 {
			t.timeout = C.DefaultURLTestStickyTimeout
		}
		t.cap = options.Cap
		if t.cap == 0 {
			t.cap = C.DefaultURLTestStickyCap
		}
		t.entries = make(map[string]*stickyEntry)
		t.ticker = time.NewTicker(t.timeout)
		t.done = make(chan struct{})
		// Pass the channels by value so the sweeper never reads t.ticker/t.done after
		// close() mutates them under the lock (mirrors URLTestGroup.loopCheck).
		go t.sweepLoop(t.ticker.C, t.done)
	}
	return t, nil
}

func (t *stickyTable) key(ctx context.Context, destination M.Socksaddr) string {
	return t.builder.build(ctx, destination)
}

// lookup resolves key to a live node. For jumphash it is a pure function of (key, live).
// For ttlmap it returns the bound node when alive, otherwise picks via repick, records
// the result, and re-pins on a dead node.
func (t *stickyTable) lookup(key string, live []adapter.Outbound, repick func([]adapter.Outbound) adapter.Outbound) adapter.Outbound {
	if t.mode == C.URLTestStickyJumpHash {
		return live[jumpConsistentHash(hashKey(key), len(live))]
	}
	now := time.Now()
	t.access.Lock()
	defer t.access.Unlock()
	if entry, ok := t.entries[key]; ok {
		if node := findLive(entry.tag, live); node != nil {
			entry.lastTime = now
			return node
		}
		// Bound node died — re-pin to a fresh choice.
		node := repick(live)
		entry.tag = node.Tag()
		entry.lastTime = now
		return node
	}
	node := repick(live)
	t.entries[key] = &stickyEntry{tag: node.Tag(), lastTime: now}
	t.evictLocked(now)
	return node
}

// evictLocked drops expired entries and enforces the cap (oldest-first). Caller holds access.
func (t *stickyTable) evictLocked(now time.Time) {
	for key, entry := range t.entries {
		if now.Sub(entry.lastTime) > t.timeout {
			delete(t.entries, key)
		}
	}
	for len(t.entries) > t.cap {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, entry := range t.entries {
			if first || entry.lastTime.Before(oldest) {
				oldestKey = key
				oldest = entry.lastTime
				first = false
			}
		}
		delete(t.entries, oldestKey)
	}
}

func (t *stickyTable) sweepLoop(tick <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-tick:
			now := time.Now()
			t.access.Lock()
			t.evictLocked(now)
			t.access.Unlock()
		}
	}
}

func (t *stickyTable) close() {
	if t.mode != C.URLTestStickyTTLMap {
		return
	}
	t.access.Lock()
	defer t.access.Unlock()
	if t.ticker == nil {
		return
	}
	t.ticker.Stop()
	t.ticker = nil
	close(t.done)
}

func findLive(tag string, live []adapter.Outbound) adapter.Outbound {
	for _, node := range live {
		if node.Tag() == tag {
			return node
		}
	}
	return nil
}

// --- hashing ------------------------------------------------------------------------

func hashKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// jumpConsistentHash is the Lamping–Veach jump consistent hash: maps key to a bucket in
// [0, numBuckets) such that adding/removing a bucket remaps only ~1/n of keys.
func jumpConsistentHash(key uint64, numBuckets int) int {
	if numBuckets <= 1 {
		return 0
	}
	var b, j int64 = -1, 0
	for j < int64(numBuckets) {
		b = j
		key = key*2862933555777941757 + 1
		j = int64(float64(b+1) * (float64(int64(1)<<31) / float64((key>>33)+1)))
	}
	return int(b)
}

// liveOutbounds returns the tag-sorted live nodes for network: those with a fresh
// URL-test history that support the network. The sort makes jumphash deterministic.
func (g *URLTestGroup) liveOutbounds(network string) []adapter.Outbound {
	live := make([]adapter.Outbound, 0, len(g.outbounds))
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		if g.history.LoadURLTestHistory(RealTag(detour)) == nil {
			continue
		}
		live = append(live, detour)
	}
	sort.Slice(live, func(i, j int) bool {
		return live[i].Tag() < live[j].Tag()
	})
	return live
}
