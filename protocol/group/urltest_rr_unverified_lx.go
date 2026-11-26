package group

// lx:begin SPEC 019, SPEC 138 the pool nodes the manual test left unverified
//
// The manual test erases history as upstream, also when no probe of it answered:
// the node that failed it shows as an error. Such a test has no control (A0):
// nothing in it tells a dead node from a dead network. The pool nodes that lost
// their history to it are unverified. The slot of an unverified node is not an
// empty slot: the pick skips it (no history), but neither the replacement search
// nor the fill of a run gives it away. The first round after it verifies them with
// a control: the check of the pool takes them as pool nodes, so a node that answers
// keeps its slot and a node that fails while the control answers is struck out
// (D1); a run with an answer probed them with that answer as the control. A mark
// goes once its node has history again or leaves its slot, and when a run with an
// answer ends. A run with no answer and a network change keep the marks.

import (
	"sync"
)

// rrUnverified is the set of unverified pool nodes; it lives in the balancer.
type rrUnverified struct {
	access sync.Mutex
	tags   map[string]bool
}

func (u *rrUnverified) mark(tags []string) {
	if len(tags) == 0 {
		return
	}
	u.access.Lock()
	defer u.access.Unlock()
	if u.tags == nil {
		u.tags = make(map[string]bool, len(tags))
	}
	for _, tag := range tags {
		u.tags[tag] = true
	}
}

func (u *rrUnverified) unmark(tag string) {
	u.access.Lock()
	defer u.access.Unlock()
	delete(u.tags, tag)
}

func (u *rrUnverified) clear() {
	u.access.Lock()
	defer u.access.Unlock()
	u.tags = nil
}

func (u *rrUnverified) marked(tag string) bool {
	u.access.Lock()
	defer u.access.Unlock()
	return u.tags[tag]
}

// rrMarkUnverified marks the pool members of a batch of the manual test with no
// answer whose history its failed probes erased. The set only grows: a second
// manual test in the same outage finds no history to erase, and its nodes stay
// marked.
func (g *URLTestGroup) rrMarkUnverified(batch *rrBatch) {
	b := g.balancer
	var tags []string
	for member := range batch.takeErased() {
		if b.inPool(member) && !b.isQuarantined(member) && !g.rrHasHistory(member) {
			tags = append(tags, member)
		}
	}
	b.unverified.mark(tags)
}

// rrUnverified reports whether the slot node tag is unverified: marked, still in
// its slot, without history, not in quarantine. A mark whose node has history
// again or left its slot goes.
func (g *URLTestGroup) rrUnverified(tag string) bool {
	b := g.balancer
	if b == nil || tag == "" || !b.unverified.marked(tag) {
		return false
	}
	if !b.inPool(tag) || g.rrHasHistory(tag) {
		b.unverified.unmark(tag)
		return false
	}
	return !b.isQuarantined(tag)
}

// rrSlotToVerify reports a slot whose node is unverified: the check of the pool
// verifies it.
func (s *URLTest) rrSlotToVerify() bool {
	for _, entry := range s.balancer.slotsSnapshot() {
		if s.group.rrUnverified(entry.tag) {
			return true
		}
	}
	return false
}

// rrSlotFillable reports a slot the replacement and the fill of a run may give
// away: empty (rrSlotEmpty) and its node not unverified.
func (s *URLTest) rrSlotFillable(tag string) bool {
	return s.rrSlotEmpty(tag) && !s.group.rrUnverified(tag)
}

// lx:end SPEC 019, SPEC 138
