package wireguard

// lx: SPEC 097 — the build budget: at most lx.wg.build_max WG/AWG devices
// built at once.
//
// "Built" means the endpoint has a tun device (SPEC 020 levels 0–2); sleeping
// at level 1–2 does not free a slot, only a teardown does. A rebuild (level 3 →
// 0, including the first build of a lazy endpoint) takes a slot with Acquire;
// when none is free, Acquire tears down a victim, and when there is no victim
// it waits (build_overflow "wait") or builds over the cap with a warning
// ("build").
//
// Victim order (owner's decision 2026-09-24), lexicographic: devices built over
// the cap first; then fewest manual refs (selector choice, final, rule target);
// then fewest auto refs (urltest pool, detour, chain position, DNS detour);
// then the longest time since the last dial. Traffic does not rank; it only
// guards — an endpoint with a dial in flight, established TCP flows or recent
// transfer is never torn down.
//
// Lock order: requester.resumeMu → budget.mu (short) → victim.resumeMu, and
// budget.mu is never held while calling into an endpoint. Only a torn-down
// endpoint acquires, and a torn-down endpoint is never a victim, so no cycle
// of resumeMu waits can form.

import (
	"context"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

const (
	// budgetRecheckInterval re-runs victim selection while waiting: a flow
	// ending signals nothing, so the waiter polls as well as listening.
	budgetRecheckInterval = time.Second
	// budgetResampleDelay is the pause before a second transfer sample of the
	// candidates the first sample refused for traffic alone (evictionTraffic).
	budgetResampleDelay = 250 * time.Millisecond
)

// BuildBudget caps the number of simultaneously built WG/AWG devices of one box.
// A nil *BuildBudget is valid and does nothing.
type BuildBudget struct {
	ctx      context.Context
	max      int
	overflow option.LXBuildOverflow

	mu sync.Mutex
	// built: endpoints with a device. building: endpoints that hold a slot for
	// a rebuild in progress. evicting: victims being torn down by some
	// requester, invisible to other requesters. overBudget: endpoints built
	// over the cap (build_overflow "build"), evicted first.
	built      map[*Endpoint]struct{}
	building   map[*Endpoint]struct{}
	evicting   map[*Endpoint]struct{}
	overBudget map[*Endpoint]struct{}
	// changed is closed and replaced on every transition a waiter may care
	// about: a slot released, a build finished, an endpoint fell asleep.
	changed chan struct{}
}

// NewBuildBudget creates the budget of one box. build_max 0 means no cap: the
// registry is still kept, for observability.
func NewBuildBudget(ctx context.Context, wg option.LXWGResolved) *BuildBudget {
	return &BuildBudget{
		ctx:        ctx,
		max:        wg.BuildMax,
		overflow:   wg.BuildOverflow,
		built:      make(map[*Endpoint]struct{}),
		building:   make(map[*Endpoint]struct{}),
		evicting:   make(map[*Endpoint]struct{}),
		overBudget: make(map[*Endpoint]struct{}),
		changed:    make(chan struct{}),
	}
}

// Acquire takes a build slot for self, a torn-down endpoint about to rebuild,
// called under self.resumeMu. It returns nil once self holds the slot (Register
// or Release must follow), or an error when ctx ends first (wait) or self is
// closing.
func (b *BuildBudget) Acquire(ctx context.Context, self *Endpoint) error {
	if b == nil {
		return nil
	}
	resampled := false
	for {
		if self.closing.Load() {
			return os.ErrClosed
		}
		b.mu.Lock()
		count := len(b.built) + len(b.building)
		if b.max <= 0 || count < b.max {
			b.building[self] = struct{}{}
			b.mu.Unlock()
			return nil
		}
		candidates := b.candidatesLocked(self)
		wake := b.changed
		b.mu.Unlock()

		evicted, trafficOnly := b.evictOne(b.rank(candidates))
		if evicted {
			continue
		}
		resample := trafficOnly && !resampled
		if b.overflow == option.LXBuildOverflowBuild && !resample {
			b.mu.Lock()
			count = len(b.built) + len(b.building)
			if count < b.max {
				b.mu.Unlock()
				continue
			}
			b.building[self] = struct{}{}
			b.overBudget[self] = struct{}{}
			b.mu.Unlock()
			self.logger.Warn("lx idle: build over budget ", count+1, "/", b.max, " ", self.Tag())
			return nil
		}
		interval := budgetRecheckInterval
		if resample {
			interval = budgetResampleDelay
			resampled = true
		}
		timer := time.NewTimer(interval)
		select {
		case <-wake:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return E.New("build budget exhausted (", count, " built, none idle)")
		}
	}
}

type budgetCandidate struct {
	endpoint   *Endpoint
	overBudget bool
	manual     int
	auto       int
	idle       time.Duration
}

// candidatesLocked lists the built endpoints another requester may tear down.
func (b *BuildBudget) candidatesLocked(self *Endpoint) []budgetCandidate {
	candidates := make([]budgetCandidate, 0, len(b.built))
	for endpoint := range b.built {
		if endpoint == self {
			continue
		}
		if _, busy := b.evicting[endpoint]; busy {
			continue
		}
		_, overBudget := b.overBudget[endpoint]
		candidates = append(candidates, budgetCandidate{endpoint: endpoint, overBudget: overBudget})
	}
	return candidates
}

// rank orders candidates, most evictable first. Runs without budget.mu: the
// refs come from the router's cached walk, which takes group locks.
func (b *BuildBudget) rank(candidates []budgetCandidate) []budgetCandidate {
	reporter := b.reachability()
	for i := range candidates {
		if reporter != nil {
			candidates[i].manual, candidates[i].auto = reporter.SelectionRefs(candidates[i].endpoint.Tag())
		}
		candidates[i].idle = math.MaxInt64
		if candidates[i].endpoint.lastActivity.Load() != 0 {
			candidates[i].idle = candidates[i].endpoint.IdleSince()
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.overBudget != right.overBudget {
			return left.overBudget
		}
		if left.manual != right.manual {
			return left.manual < right.manual
		}
		if left.auto != right.auto {
			return left.auto < right.auto
		}
		return left.idle > right.idle
	})
	return candidates
}

// evictOne tries the ranked candidates in order and tears down the first one
// that is still a candidate under its own lock. trafficOnly reports that some
// candidate was refused for recent transfer alone.
func (b *BuildBudget) evictOne(ranked []budgetCandidate) (evicted bool, trafficOnly bool) {
	for _, candidate := range ranked {
		victim := candidate.endpoint
		b.mu.Lock()
		_, stillBuilt := b.built[victim]
		_, busy := b.evicting[victim]
		if !stillBuilt || busy {
			b.mu.Unlock()
			continue
		}
		b.evicting[victim] = struct{}{}
		b.mu.Unlock()

		verdict := victim.TeardownForBudget()

		b.mu.Lock()
		delete(b.evicting, victim)
		b.mu.Unlock()
		switch verdict {
		case evictionDone:
			return true, false
		case evictionTraffic:
			trafficOnly = true
		}
	}
	return false, trafficOnly
}

func (b *BuildBudget) reachability() adapter.ReachabilityReporter {
	if b.ctx == nil {
		return nil
	}
	return service.FromContext[adapter.ReachabilityReporter](b.ctx)
}

// Register records that endpoint now has a device: after a rebuild that took
// a slot, or a device built at start (which never asked — lx.wg.build_max
// applies from the first rebuild).
func (b *BuildBudget) Register(endpoint *Endpoint) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.building, endpoint)
	b.built[endpoint] = struct{}{}
	b.notifyLocked()
	b.mu.Unlock()
}

// Release frees endpoint's slot: its device was torn down or closed, or its
// rebuild failed. Idempotent.
func (b *BuildBudget) Release(endpoint *Endpoint) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.built, endpoint)
	delete(b.building, endpoint)
	delete(b.overBudget, endpoint)
	b.notifyLocked()
	b.mu.Unlock()
}

// Notify wakes the waiters to re-run victim selection: an endpoint fell asleep,
// or one of them is closing.
func (b *BuildBudget) Notify() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.notifyLocked()
	b.mu.Unlock()
}

func (b *BuildBudget) notifyLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// BuildBudgetSnapshot is the budget's registry at one moment.
type BuildBudgetSnapshot struct {
	Max        int
	Built      []string
	Building   []string
	OverBudget []string
}

// Snapshot returns the registry, tags sorted. Zero value for a nil budget.
func (b *BuildBudget) Snapshot() BuildBudgetSnapshot {
	if b == nil {
		return BuildBudgetSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return BuildBudgetSnapshot{
		Max:        b.max,
		Built:      budgetTags(b.built),
		Building:   budgetTags(b.building),
		OverBudget: budgetTags(b.overBudget),
	}
}

func budgetTags(set map[*Endpoint]struct{}) []string {
	tags := make([]string, 0, len(set))
	for endpoint := range set {
		tags = append(tags, endpoint.Tag())
	}
	sort.Strings(tags)
	return tags
}
