package group

// lx:begin SPEC 138 round_robin: the probe queue of a core instance (§4.7)
//
// The rules named here are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/
// ALGORITHM.md. One queue per core instance, shared by every round_robin group of
// it: at most rrQueueCap (U) self-heal probes in flight — the checks, the
// replacement searches and the life probes. The scheduled run stays outside, it has
// its own limit. The queue only orders and paces: no inference is made in it, the
// pauses (W), the quarantine and the counts stay with the group.
//   - U1: a group asks for a batch of places and gets all of them at once; a batch
//     larger than U is asked for in parts (rrCheck.acquire).
//   - U2: the groups waiting are served in turn, one batch each.
//   - U3: one probe per node (leaf and URL) in flight per core. A probe asked for
//     while one of the same node flies takes its outcome when that one started not
//     earlier than the lower bound of the rule asking; otherwise it waits for its
//     end and starts its own. The place of the asker is held all along. A flight
//     whose askers all gave up is cancelled and leaves the table at once: nobody
//     joins a cancelled probe, and a cancelled probe is no outcome (rrProbe.cancelled).
//     The decisive probe of a node whose own first probe still flies does not wait
//     for it and flies beside it (rrCheck.launch).
//
// The core instance is the owner of the shared URL test history: the queue is kept
// by a weak pointer to the history storage and dropped with it, so two instances
// in one process (and tests) never share one.

import (
	"context"
	"runtime"
	"sync"
	"time"
	"weak"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	N "github.com/sagernet/sing/common/network"
)

// rrQueueCap is U: self-heal probes in flight per core instance.
const rrQueueCap = 16

// rrQueue is the probe queue of one core instance.
type rrQueue struct {
	access  sync.Mutex
	held    int
	ring    []*rrQueueOwner // groups with a request waiting, in their turn order
	flights map[rrFlightKey]*rrFlight
}

// rrQueueOwner is the place of one group in the queue; it lives in rrHeal.
type rrQueueOwner struct {
	waiting []*rrQueueRequest
	queued  bool
}

type rrQueueRequest struct {
	places  int
	granted chan struct{}
	ok      bool
}

type rrFlightKey struct {
	leaf string
	link string
}

type rrFlight struct {
	key     rrFlightKey
	started time.Time
	done    chan struct{}
	result  rrProbe
	waiters int
	cancel  context.CancelFunc
}

var rrQueues struct {
	access sync.Mutex
	byCore map[weak.Pointer[urltest.HistoryStorage]]*rrQueue
}

// rrQueueOf returns the queue of the core instance that owns history.
func rrQueueOf(history *urltest.HistoryStorage) *rrQueue {
	key := weak.Make(history)
	rrQueues.access.Lock()
	defer rrQueues.access.Unlock()
	if queue := rrQueues.byCore[key]; queue != nil {
		return queue
	}
	if rrQueues.byCore == nil {
		rrQueues.byCore = make(map[weak.Pointer[urltest.HistoryStorage]]*rrQueue)
	}
	queue := &rrQueue{flights: make(map[rrFlightKey]*rrFlight)}
	rrQueues.byCore[key] = queue
	runtime.AddCleanup(history, func(key weak.Pointer[urltest.HistoryStorage]) {
		rrQueues.access.Lock()
		delete(rrQueues.byCore, key)
		rrQueues.access.Unlock()
	}, key)
	return queue
}

// acquire waits for places of owner (U1, U2); false when ctx ended first.
func (q *rrQueue) acquire(ctx context.Context, owner *rrQueueOwner, places int) bool {
	if places <= 0 {
		return true
	}
	if places > rrQueueCap {
		places = rrQueueCap
	}
	request := &rrQueueRequest{places: places, granted: make(chan struct{})}
	q.access.Lock()
	owner.waiting = append(owner.waiting, request)
	if !owner.queued {
		owner.queued = true
		q.ring = append(q.ring, owner)
	}
	q.dispatch()
	q.access.Unlock()
	select {
	case <-request.granted:
		return true
	case <-ctx.Done():
	}
	q.access.Lock()
	defer q.access.Unlock()
	if request.ok {
		q.held -= places
		q.dispatch()
		return false
	}
	for i, waiting := range owner.waiting {
		if waiting == request {
			owner.waiting = append(owner.waiting[:i], owner.waiting[i+1:]...)
			break
		}
	}
	if len(owner.waiting) == 0 && owner.queued {
		owner.queued = false
		for i, queued := range q.ring {
			if queued == owner {
				q.ring = append(q.ring[:i], q.ring[i+1:]...)
				break
			}
		}
	}
	q.dispatch()
	return false
}

// release gives back places.
func (q *rrQueue) release(places int) {
	if places <= 0 {
		return
	}
	q.access.Lock()
	q.held -= places
	q.dispatch()
	q.access.Unlock()
}

// dispatch grants the head batch of the group whose turn it is while it fits (U2);
// the caller holds access.
func (q *rrQueue) dispatch() {
	for len(q.ring) > 0 {
		owner := q.ring[0]
		request := owner.waiting[0]
		if q.held+request.places > rrQueueCap {
			return
		}
		q.held += request.places
		request.ok = true
		close(request.granted)
		owner.waiting = owner.waiting[1:]
		q.ring = q.ring[1:]
		if len(owner.waiting) > 0 {
			q.ring = append(q.ring, owner)
		} else {
			owner.queued = false
		}
	}
}

// inFlight returns the places held (for tests and the status).
func (q *rrQueue) inFlight() int {
	q.access.Lock()
	defer q.access.Unlock()
	return q.held
}

// probe runs run for key, or takes the outcome of the probe of key in flight that
// started not earlier than from (U3). The probe runs on a context that keeps the
// values of ctx and ends when every asker gave up.
func (q *rrQueue) probe(ctx context.Context, key rrFlightKey, from time.Time, run func(ctx context.Context) rrProbe) rrProbe {
	for {
		q.access.Lock()
		flight := q.flights[key]
		if flight == nil {
			probeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			flight = &rrFlight{key: key, started: time.Now(), done: make(chan struct{}), waiters: 1, cancel: cancel}
			q.flights[key] = flight
			q.access.Unlock()
			go func() {
				result := run(probeCtx)
				cancel()
				q.access.Lock()
				flight.result = result
				if q.flights[key] == flight {
					delete(q.flights, key)
				}
				q.access.Unlock()
				close(flight.done)
			}()
			return q.wait(ctx, flight)
		}
		if !flight.started.Before(from) {
			flight.waiters++
			q.access.Unlock()
			return q.wait(ctx, flight)
		}
		q.access.Unlock()
		select {
		case <-flight.done:
		case <-ctx.Done():
			return rrProbe{err: ctx.Err(), started: time.Now(), ended: time.Now()}
		}
	}
}

func (q *rrQueue) wait(ctx context.Context, flight *rrFlight) rrProbe {
	select {
	case <-flight.done:
		return flight.result
	case <-ctx.Done():
	}
	q.access.Lock()
	flight.waiters--
	if flight.waiters == 0 {
		// Nobody waits for it: cancelled, it leaves the table now, so that a new asker
		// starts a probe of its own instead of taking the cancel as an outcome.
		flight.cancel()
		if q.flights[flight.key] == flight {
			delete(q.flights, flight.key)
		}
	}
	q.access.Unlock()
	return rrProbe{err: ctx.Err(), started: time.Now(), ended: time.Now()}
}

// rrQueue returns the queue of the core instance of the group.
func (g *URLTestGroup) rrQueue() *rrQueue {
	return rrQueueOf(g.history)
}

// rrQueuedProbe probes member through the queue's U3 rule; the caller holds its
// place. from is the lower bound of the start the rule asking needs.
func (g *URLTestGroup) rrQueuedProbe(ctx context.Context, member adapter.Outbound, from time.Time) rrProbe {
	return g.rrHealProbe(ctx, member, from, false)
}

// rrHealProbe is rrQueuedProbe; alone starts a probe of its own beside the flight
// of the node: the decisive probe does not wait for the group's own first probe of
// the node (the exception of U3, step 3 of §4.2).
func (g *URLTestGroup) rrHealProbe(ctx context.Context, member adapter.Outbound, from time.Time, alone bool) rrProbe {
	leaf := RealTag(member, N.NetworkTCP)
	if leaf == "" || alone {
		return g.rrProbeNode(ctx, member)
	}
	result := g.rrQueue().probe(ctx, rrFlightKey{leaf: leaf, link: g.link}, from, func(ctx context.Context) rrProbe {
		return g.rrProbeNode(ctx, member)
	})
	result.member, result.leaf = member.Tag(), leaf
	return result
}

// lx:end SPEC 138
