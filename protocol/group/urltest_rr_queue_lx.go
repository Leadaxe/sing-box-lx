package group

// lx:begin SPEC 138 round_robin: the probe queue of a core instance (§4.7)
//
// The rules are those of SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/ALGORITHM.md §4.7
// (U1–U3). One queue per core instance, shared by every round_robin group of it; the
// scheduled run stays outside. The queue only orders and paces: no inference is made
// in it. A request larger than U is asked for in parts (rrCheck.launchAll). A flight
// whose askers all gave up is cancelled and leaves the table at once, so nobody joins
// a cancelled probe; the decisive probe of a node whose own first probe still flies
// flies beside it (rrCheck.launch).
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
	granted func() // run at the grant under access; never blocks
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
	granted := make(chan struct{})
	request := q.ask(owner, places, func() { close(granted) })
	select {
	case <-granted:
		return true
	case <-ctx.Done():
	}
	if !q.withdraw(owner, request) {
		q.release(request.places) // granted meanwhile: the places go back
	}
	return false
}

// ask asks for places of owner without waiting (U1, U2): granted runs once they are
// granted, under the lock of the queue, and must not block. The request takes its
// place in the line at once, in the order of the calls: a part of the decisive
// probes asked for after the first probes of a pool larger than U must not overtake
// them (its places would wait for the first probes of its own nodes, which would
// wait for its places). A request not granted yet is taken back by withdraw.
func (q *rrQueue) ask(owner *rrQueueOwner, places int, granted func()) *rrQueueRequest {
	request := &rrQueueRequest{places: min(places, rrQueueCap), granted: granted}
	if places <= 0 {
		request.ok = true
		granted()
		return request
	}
	q.access.Lock()
	defer q.access.Unlock()
	owner.waiting = append(owner.waiting, request)
	if !owner.queued {
		owner.queued = true
		q.ring = append(q.ring, owner)
	}
	q.dispatch()
	return request
}

// withdraw takes back a request of owner that has not been granted; false when it
// has been: its places are held.
func (q *rrQueue) withdraw(owner *rrQueueOwner, request *rrQueueRequest) bool {
	q.access.Lock()
	defer q.access.Unlock()
	if request.ok {
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
	return true
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
		request.granted()
		owner.waiting = owner.waiting[1:]
		q.ring = q.ring[1:]
		if len(owner.waiting) > 0 {
			q.ring = append(q.ring, owner)
		} else {
			owner.queued = false
		}
	}
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

// rrSelfProbe is a probe of the self-heal of member; the caller holds its place in
// the queue. It takes the outcome of the flight of the node by U3, from being the
// lower bound of the start the rule asking needs; alone starts a probe of its own
// beside the flight: the decisive probe does not wait for the group's own first
// probe of the node (the exception of U3, step 3 of §4.2).
//
// A probe that ended with no ANSWER (and was not cancelled) takes the liveness from
// the history of its leaf written before it (§5): the history is not erased — that
// would be an inference with no control (A0).
func (g *URLTestGroup) rrSelfProbe(ctx context.Context, member adapter.Outbound, from time.Time, alone bool) rrProbe {
	var result rrProbe
	if leaf := RealTag(member, N.NetworkTCP); leaf == "" || alone {
		result = g.rrProbeNode(ctx, member)
	} else {
		result = g.rrQueue().probe(ctx, rrFlightKey{leaf: leaf, link: g.link}, from, func(ctx context.Context) rrProbe {
			return g.rrProbeNode(ctx, member)
		})
		result.member, result.leaf = member.Tag(), leaf
		if g.balancer != nil {
			g.balancer.rrClockLook(time.Now()) // N3: every group that takes the outcome compares its clocks
		}
	}
	if g.balancer != nil && result.leaf != "" && !result.answered && !result.cancelled() {
		g.balancer.heal.missed(result.leaf, result.ended)
	}
	return result
}

// lx:end SPEC 138
