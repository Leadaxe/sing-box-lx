// lx:begin SPEC 138 round_robin self-heal tests: the fixture
//
// The tests reach the state of the round and the pause, the probe queue and the
// slot counters only through these helpers, so that a change of how the state is
// kept touches this file, not every test.

package group

import "time"

// roundBusy reports a round of the group in flight.
func (h *rrHeal) roundBusy() bool { return h.running.Load() }

// holdRound takes the round of the group outside beginRoundAt: a round the test
// does not want to start.
func (h *rrHeal) holdRound() { h.running.Store(true) }

// tryHoldRound takes the round when it is free.
func (h *rrHeal) tryHoldRound() bool { return h.running.CompareAndSwap(false, true) }

// dropRound gives back a round taken by holdRound.
func (h *rrHeal) dropRound() { h.running.Store(false) }

// pauseEnd returns e, the end of the previous round (nil: no pause).
func (h *rrHeal) pauseEnd() *time.Time { return h.lastCheckEnd.Load() }

// setPauseEnd sets e.
func (h *rrHeal) setPauseEnd(end *time.Time) { h.lastCheckEnd.Store(end) }

// clearPause ends the pause: what the passing time does.
func (h *rrHeal) clearPause() { h.lastCheckEnd.Store(nil) }

// pauseStep returns the step of the pause.
func (h *rrHeal) pauseStep() int32 { return h.step.Load() }

// setPauseStep sets the step of the pause.
func (h *rrHeal) setPauseStep(step int32) { h.step.Store(step) }

// pollCount returns o.
func (h *rrHeal) pollCount() int32 { return h.polled.Load() }

// setPollCount sets o.
func (h *rrHeal) setPollCount(polled int32) { h.polled.Store(polled) }

// inFlight returns the places of the queue held.
func (q *rrQueue) inFlight() int {
	q.access.Lock()
	defer q.access.Unlock()
	return q.held
}

// lx:end SPEC 138
