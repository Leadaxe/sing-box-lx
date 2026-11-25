package group

// lx:begin SPEC 138 round_robin self-heal state for GetPool
//
// The self-heal state the log tells about, as numbers for the clients: per slot
// the quarantine and the slot counters, per group the shifted and marked keys
// and the freeze. Read-only snapshots under the locks and atomics the state
// already has; nothing here runs on the pick path.

import (
	"time"
)

// PoolHeal is the group-wide self-heal state of a round_robin group (SPEC 138
// §3.5, §3.7). Zero for least_test and failover.
type PoolHeal struct {
	ShiftedKeys int // keys of the shift table with a shift and no address-problem mark
	MarkedKeys  int // keys marked as an address problem
	Frozen      bool
	FrozenFor   time.Duration // what is left of the freeze
}

// PoolHeal returns the group-wide self-heal state; zero outside round_robin.
func (s *URLTest) PoolHeal() PoolHeal {
	if s.balancer == nil {
		return PoolHeal{}
	}
	return s.balancer.rrHealState(time.Now())
}

func (b *balancer) rrHealState(now time.Time) PoolHeal {
	var state PoolHeal
	table := &b.memory.shifts
	if table.size.Load() > 0 {
		table.access.RLock()
		for _, e := range table.entries {
			if e.marked {
				state.MarkedKeys++
			} else if e.shift != 0 {
				state.ShiftedKeys++
			}
		}
		table.access.RUnlock()
	}
	if end := b.heal.epochEnd.Load(); end != 0 && end > rrClock(now) {
		left := time.Duration(end - rrClock(now))
		state.Frozen, state.FrozenFor = true, left
	}
	return state
}

// rrSlotStates fills the quarantine and the counters of every slot of a Pool()
// snapshot: one pass under the counters' lock. A node that left the pool since
// the snapshot reads as zero.
func (b *balancer) rrSlotStates(slots []PoolSlot) {
	m := &b.memory
	quarantine := m.quarantine.Load()
	m.access.Lock()
	defer m.access.Unlock()
	for i := range slots {
		if quarantine != nil {
			slots[i].Quarantined = (*quarantine)[slots[i].Tag]
		}
		if c := m.counts[slots[i].Tag]; c != nil {
			slots[i].Conns, slots[i].Fails = c.conns, c.fails
		}
	}
}

// lx:end SPEC 138
