// lx:begin SPEC 138 round_robin self-heal: the fixes of review 9 (ALGORITHM.md §4.2, §4.6, §4.7, §5)

package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/urltest"
)

// waitLong waits up to d for cond.
func waitLong(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// U3: a flight cancelled because its askers left is not joined by a new asker; the
// new one starts a probe of its own.
func TestRRReview9_cancelledFlightNotJoined(t *testing.T) {
	q := rrQueueOf(urltest.NewHistoryStorage())
	key := rrFlightKey{leaf: "n", link: "u"}
	run := func(ctx context.Context) rrProbe {
		started := time.Now()
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // the dial notices the cancel a little later
		return rrProbe{err: ctx.Err(), started: started, ended: time.Now()}
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	go q.probe(ctxA, key, time.Time{}, run)
	time.Sleep(10 * time.Millisecond)
	cancelA()
	time.Sleep(10 * time.Millisecond)
	answered := func(ctx context.Context) rrProbe { return rrProbe{answered: true, started: time.Now()} }
	got := q.probe(context.Background(), key, time.Time{}, answered)
	if !got.answered {
		t.Fatalf("a new asker took the outcome of a flight cancelled by the others: err=%v", got.err)
	}
}

// U3, D1: a cancelled decisive probe is no "no ANSWER": its part gets no control
// after for it and the node is not struck.
func TestRRReview9_cancelledDecisiveIsNoMiss(t *testing.T) {
	c := &rrCheck{
		queue:      rrQueueOf(urltest.NewHistoryStorage()),
		answered:   make([]bool, 1),
		firstEnded: []bool{true},
		voided:     make([]bool, 1),
		partOf:     []int{0},
		parts:      []*rrDecisivePart{{open: 1}},
		reserved:   1,
	}
	c.take(rrCheckResult{role: rrRoleDecisive, index: 0, probe: rrProbe{err: context.Canceled}})
	if c.parts[0].missed || !c.voided[0] || c.flying != 0 {
		t.Fatalf("a cancelled decisive probe counts as a miss: missed=%v voided=%v flying=%d", c.parts[0].missed, c.voided[0], c.flying)
	}
}

// lx:end SPEC 138
