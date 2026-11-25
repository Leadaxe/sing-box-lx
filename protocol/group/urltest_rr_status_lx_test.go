// lx:begin SPEC 138 round_robin self-heal state in Pool()/PoolHeal() tests

package group

import (
	"context"
	"testing"
	"time"
)

func TestRRStatus_poolAndHeal(t *testing.T) {
	f := newHealFixture(t, []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0)
	b := f.s.balancer
	for _, tag := range []string{"a", "b", "c"} {
		for i := 0; i < 10; i++ {
			b.rrCountConn(tag, time.Now())
		}
	}
	var verdict rrQuarantineVerdict
	for i := 0; i < rrQuarantineMinFails; i++ {
		verdict = b.rrCountFail("a", rrKey{})
	}
	if !verdict.quarantined {
		t.Fatalf("a fails while its neighbours do not: %+v", verdict)
	}
	b.rrCountFail("b", rrKey{})
	table := &b.memory.shifts
	table.access.Lock()
	table.entry(1).shift = 1
	table.entry(2).shift = 2
	table.entry(3).marked = true
	table.entry(4) // an entry without a shift counts nowhere
	table.access.Unlock()

	slots := f.s.Pool()
	if len(slots) != 3 {
		t.Fatalf("pool: %+v", slots)
	}
	want := map[string]PoolSlot{
		"a": {Quarantined: true}, // Q2: the counters start from zero
		"b": {Conns: 10, Fails: 1},
		"c": {Conns: 10},
	}
	for _, slot := range slots {
		w := want[slot.Tag]
		if slot.Quarantined != w.Quarantined || slot.Conns != w.Conns || slot.Fails != w.Fails {
			t.Fatalf("slot %s: %+v, want %+v", slot.Tag, slot, w)
		}
	}
	heal := f.s.PoolHeal()
	if heal.ShiftedKeys != 2 || heal.MarkedKeys != 1 || heal.Frozen || heal.FrozenFor != 0 {
		t.Fatalf("heal: %+v", heal)
	}

	b.heal.freeze()
	heal = f.s.PoolHeal()
	if !heal.Frozen || heal.FrozenFor <= 0 || heal.FrozenFor > rrHealFreeze {
		t.Fatalf("frozen: %+v", heal)
	}
	b.heal.epochEnd.Store(rrClock(time.Now()))
	if heal = f.s.PoolHeal(); heal.Frozen {
		t.Fatalf("the freeze ended: %+v", heal)
	}

	// The scheduled run lifts the quarantine and the marks; the shifts stay and a
	// node out of quarantine starts its counters from zero.
	f.g.balancePool(context.Background())
	for _, slot := range f.s.Pool() {
		if slot.Quarantined {
			t.Fatalf("the scheduled run lifts the quarantine: %+v", slot)
		}
		if slot.Tag == "a" && (slot.Conns != 0 || slot.Fails != 0) {
			t.Fatalf("a out of quarantine starts from zero: %+v", slot)
		}
		if slot.Tag == "b" && (slot.Conns != 10 || slot.Fails != 1) {
			t.Fatalf("b keeps its counters: %+v", slot)
		}
	}
	if heal = f.s.PoolHeal(); heal.ShiftedKeys != 2 || heal.MarkedKeys != 0 {
		t.Fatalf("after the scheduled run: %+v", heal)
	}
}

func TestRRStatus_notRoundRobin(t *testing.T) {
	s := &URLTest{}
	if slots := s.Pool(); slots != nil {
		t.Fatalf("least_test has no pool: %+v", slots)
	}
	if heal := s.PoolHeal(); heal != (PoolHeal{}) {
		t.Fatalf("least_test has no self-heal: %+v", heal)
	}
}

// lx:end SPEC 138
