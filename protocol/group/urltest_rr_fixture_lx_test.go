// lx:begin SPEC 138 round_robin self-heal tests: the fixture
//
// The tests reach the state of the round and the pause, the probe queue and the
// slot counters only through these helpers, so that a change of how the state is
// kept touches this file, not every test.

package group

import "time"

// roundBusy reports a round of the group in flight.
func (h *rrHeal) roundBusy() bool {
	h.access.Lock()
	defer h.access.Unlock()
	return h.round != nil
}

// holdRound takes the round of the group outside beginRound: a round the test
// does not want to start.
func (h *rrHeal) holdRound() {
	h.access.Lock()
	defer h.access.Unlock()
	h.round = &rrRound{done: make(chan struct{})}
}

// tryHoldRound takes the round when it is free.
func (h *rrHeal) tryHoldRound() bool {
	h.access.Lock()
	defer h.access.Unlock()
	if h.round != nil {
		return false
	}
	h.round = &rrRound{done: make(chan struct{})}
	return true
}

// dropRound gives back a round taken by holdRound; no pause.
func (h *rrHeal) dropRound() {
	h.access.Lock()
	defer h.access.Unlock()
	if h.round != nil {
		close(h.round.done)
		h.round = nil
	}
}

// pauseEnd returns e, the end of the previous round (nil: no pause).
func (h *rrHeal) pauseEnd() *time.Time {
	h.access.Lock()
	defer h.access.Unlock()
	return h.end
}

// setPauseEnd sets e; another pause has no right xr yet.
func (h *rrHeal) setPauseEnd(end *time.Time) {
	h.access.Lock()
	defer h.access.Unlock()
	if end != h.end {
		h.right = rrRightNone
	}
	h.end = end
}

// clearPause ends the pause: what the passing time does.
func (h *rrHeal) clearPause() { h.setPauseEnd(nil) }

// pauseStep returns the step of the pause.
func (h *rrHeal) pauseStep() int32 {
	h.access.Lock()
	defer h.access.Unlock()
	return h.step
}

// setPauseStep sets the step of the pause.
func (h *rrHeal) setPauseStep(step int32) {
	h.access.Lock()
	defer h.access.Unlock()
	h.step = step
}

// pollCount returns o.
func (h *rrHeal) pollCount() int32 { return int32(h.polls()) }

// setPollCount sets o.
func (h *rrHeal) setPollCount(polled int32) {
	h.access.Lock()
	defer h.access.Unlock()
	h.polled = polled
}

// inFlight returns the places of the queue held.
func (q *rrQueue) inFlight() int {
	q.access.Lock()
	defer q.access.Unlock()
	return q.held
}

// lx:end SPEC 138
