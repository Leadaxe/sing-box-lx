package wireguard

// lx: SPEC 097 — the build budget: victim order, the live-flow guard, wait and
// build overflow, and the lock order under concurrent requesters. Victims are
// device-less endpoints (a zero transport endpoint: Suspend/Teardown are
// nil-safe), so the tests need neither a network nor with_gvisor.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// refsReporter stands in for the router: fixed (manual, auto) refs per tag.
type refsReporter map[string][2]int

func (r refsReporter) OutboundReachable(tag string) bool { return true }

func (r refsReporter) SelectionRefs(tag string) (int, int) {
	refs := r[tag]
	return refs[0], refs[1]
}

func newTestBudget(t *testing.T, max int, refs refsReporter) *BuildBudget {
	t.Helper()
	return newTestBudgetOverflow(t, max, refs, option.LXBuildOverflowWait)
}

func newTestBudgetOverflow(t *testing.T, max int, refs refsReporter, overflow option.LXBuildOverflow) *BuildBudget {
	t.Helper()
	ctx := context.Background()
	if refs != nil {
		ctx = service.ContextWith[adapter.ReachabilityReporter](ctx, refs)
	}
	return NewBuildBudget(ctx, option.LXWGResolved{BuildMax: max, BuildOverflow: overflow})
}

// warnLogger records Warn lines.
type warnLogger struct {
	logger.ContextLogger
	mu    sync.Mutex
	warns []string
}

func (l *warnLogger) Warn(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprint(args...))
}

func newBudgetEndpoint(tag string, budget *BuildBudget) *Endpoint {
	return &Endpoint{
		Adapter:  endpoint.NewAdapter(C.TypeWireGuard, tag, []string{N.NetworkTCP}, nil),
		ctx:      context.Background(),
		logger:   logger.NOP(),
		endpoint: &wireguard.Endpoint{},
		budget:   budget,
	}
}

// builtEndpoint registers a built endpoint, awake or asleep (level 1–2), last
// dialed idle ago.
func builtEndpoint(budget *BuildBudget, tag string, idle time.Duration, awake bool) *Endpoint {
	w := newBudgetEndpoint(tag, budget)
	w.lastActivity.Store(time.Now().Add(-idle).UnixNano())
	if awake {
		w.started.Store(true)
	} else {
		w.idleAsleep.Store(true)
		w.sleepSince.Store(time.Now().Add(-idle).UnixNano())
	}
	budget.Register(w)
	return w
}

// requester is a torn-down endpoint about to rebuild.
func requester(budget *BuildBudget, tag string) *Endpoint {
	w := newBudgetEndpoint(tag, budget)
	w.torndown.Store(true)
	w.idleAsleep.Store(true)
	return w
}

// markRebuilt simulates the successful rebuild that follows Acquire.
func markRebuilt(w *Endpoint) {
	w.torndown.Store(false)
	w.idleAsleep.Store(false)
	w.started.Store(true)
	w.stampActivity()
	w.budgetRegister()
}

func acquireWithin(t *testing.T, budget *BuildBudget, w *Endpoint, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	return budget.Acquire(ctx, w)
}

// PLAN test 4: with N=2 and a third build, the victim is the candidate with
// the lowest (manual refs, auto refs, then longest idle) — lexicographic, no
// inheritance, sleep is not a key.
func TestBuildBudget_victimOrder(t *testing.T) {
	type node struct {
		refs  [2]int
		idle  time.Duration
		awake bool
	}
	cases := []struct {
		name   string
		a, b   node
		victim string
	}{
		{"one manual ref outweighs any number of auto refs", node{[2]int{1, 0}, time.Minute, true}, node{[2]int{0, 2}, time.Minute, true}, "b"},
		{"more auto refs survive fewer", node{[2]int{0, 2}, time.Minute, true}, node{[2]int{0, 1}, time.Minute, true}, "b"},
		{"any auto ref survives none", node{[2]int{0, 1}, time.Minute, true}, node{[2]int{0, 0}, time.Minute, true}, "b"},
		{"equal refs: the longest idle goes", node{[2]int{0, 1}, time.Hour, true}, node{[2]int{0, 1}, time.Minute, true}, "a"},
		{"a sleeper with a ref survives an awake node without one", node{[2]int{0, 1}, time.Hour, false}, node{[2]int{0, 0}, time.Second, true}, "b"},
		{"all with refs: the smallest pair goes", node{[2]int{1, 1}, time.Minute, true}, node{[2]int{1, 0}, time.Minute, true}, "b"},
		{"a fresh node without refs is a legal victim", node{[2]int{0, 1}, time.Hour, true}, node{[2]int{0, 0}, 0, true}, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := newTestBudget(t, 2, refsReporter{"a": tc.a.refs, "b": tc.b.refs})
			a := builtEndpoint(budget, "a", tc.a.idle, tc.a.awake)
			b := builtEndpoint(budget, "b", tc.b.idle, tc.b.awake)
			c := requester(budget, "c")
			if err := acquireWithin(t, budget, c, time.Second); err != nil {
				t.Fatalf("acquire: %v", err)
			}
			victim, survivor := b, a
			if tc.victim == "a" {
				victim, survivor = a, b
			}
			if !victim.torndown.Load() || victim.IdleState().State != adapter.EndpointStateTornDown {
				t.Fatalf("%s must be torn down (state %q)", victim.Tag(), victim.IdleState().State)
			}
			if survivor.torndown.Load() {
				t.Fatalf("%s must survive", survivor.Tag())
			}
			if victim.started.Load() || !victim.idleAsleep.Load() {
				t.Fatal("a torn-down victim is asleep and not started")
			}
			snapshot := budget.Snapshot()
			if len(snapshot.Built) != 1 || snapshot.Built[0] != survivor.Tag() ||
				len(snapshot.Building) != 1 || snapshot.Building[0] != "c" {
				t.Fatalf("registry after eviction: %+v", snapshot)
			}
			markRebuilt(c)
			if snapshot := budget.Snapshot(); len(snapshot.Built) != 2 || len(snapshot.Building) != 0 {
				t.Fatalf("registry after the build: %+v", snapshot)
			}
		})
	}
}

// Devices built over the cap are evicted first, whatever their refs.
func TestBuildBudget_overBudgetFirst(t *testing.T) {
	budget := newTestBudgetOverflow(t, 1, refsReporter{"a": {0, 0}, "b": {1, 0}}, option.LXBuildOverflowBuild)
	a := builtEndpoint(budget, "a", time.Hour, true)
	b := requester(budget, "b")
	a.enterDial() // a is busy: b has to build over the cap
	if err := acquireWithin(t, budget, b, time.Second); err != nil {
		t.Fatal(err)
	}
	markRebuilt(b)
	a.leaveDial(struct{}{})
	if snapshot := budget.Snapshot(); len(snapshot.OverBudget) != 1 || snapshot.OverBudget[0] != "b" {
		t.Fatalf("b must be recorded over budget: %+v", snapshot)
	}
	c := requester(budget, "c")
	if err := acquireWithin(t, budget, c, time.Second); err != nil {
		t.Fatal(err)
	}
	if !b.torndown.Load() {
		t.Fatal("the over-budget device goes first, despite its manual ref")
	}
	// Still over the cap after one eviction: a goes as well.
	if !a.torndown.Load() {
		t.Fatal("the cap is restored by evicting until a slot is free")
	}
}

// PLAN test 4, guard: a node with a dial in flight is never torn down; with
// every node busy, "wait" fails at the context deadline.
func TestBuildBudget_allLive_waitTimesOut(t *testing.T) {
	budget := newTestBudget(t, 2, nil)
	a := builtEndpoint(budget, "a", time.Hour, true)
	b := builtEndpoint(budget, "b", time.Hour, false)
	a.enterDial()
	b.enterDial()
	c := requester(budget, "c")
	start := time.Now()
	err := acquireWithin(t, budget, c, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "build budget exhausted (2 built, none idle)") {
		t.Fatalf("want a budget-exhausted error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("wait must last until the deadline, returned after %v", elapsed)
	}
	if a.torndown.Load() || b.torndown.Load() {
		t.Fatal("live nodes must not be torn down")
	}
	if snapshot := budget.Snapshot(); len(snapshot.Building) != 0 {
		t.Fatalf("a failed acquire must not hold a slot: %+v", snapshot)
	}
}

// A waiter wakes when a slot is released, not only at its poll interval.
func TestBuildBudget_waitWakesOnRelease(t *testing.T) {
	budget := newTestBudget(t, 1, nil)
	a := builtEndpoint(budget, "a", time.Hour, true)
	a.enterDial()
	c := requester(budget, "c")
	result := make(chan error, 1)
	go func() { result <- acquireWithin(t, budget, c, 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	budget.Release(a)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(budgetRecheckInterval / 2):
		t.Fatal("the release must wake the waiter at once")
	}
}

// PLAN test 4, overflow: with every node busy, "build" goes over the cap with
// a warning.
func TestBuildBudget_allLive_buildOverflows(t *testing.T) {
	budget := newTestBudgetOverflow(t, 2, nil, option.LXBuildOverflowBuild)
	a := builtEndpoint(budget, "a", time.Hour, true)
	b := builtEndpoint(budget, "b", time.Hour, true)
	a.enterDial()
	b.enterDial()
	c := requester(budget, "c")
	warnings := &warnLogger{ContextLogger: logger.NOP()}
	c.logger = warnings
	if err := acquireWithin(t, budget, c, time.Second); err != nil {
		t.Fatalf("build overflow must not fail: %v", err)
	}
	if a.torndown.Load() || b.torndown.Load() {
		t.Fatal("live nodes must not be torn down")
	}
	if len(warnings.warns) != 1 || !strings.Contains(warnings.warns[0], "lx idle: build over budget 3/2") {
		t.Fatalf("want one over-budget warning, got %q", warnings.warns)
	}
}

// Candidacy is re-checked under the victim's own lock: a closing node and a
// node whose start has not finished are refused.
func TestTeardownForBudget_refusals(t *testing.T) {
	budget := newTestBudget(t, 0, nil)
	closing := builtEndpoint(budget, "closing", time.Hour, true)
	closing.closing.Store(true)
	if closing.TeardownForBudget() != evictionRefused || closing.torndown.Load() {
		t.Fatal("a closing endpoint is not a victim")
	}
	starting := newBudgetEndpoint("starting", budget)
	if starting.TeardownForBudget() != evictionRefused {
		t.Fatal("an endpoint that has not started is not a victim")
	}
	torn := builtEndpoint(budget, "torn", time.Hour, false)
	torn.torndown.Store(true)
	if torn.TeardownForBudget() != evictionRefused {
		t.Fatal("a torn-down endpoint is not a victim")
	}
	busy := builtEndpoint(budget, "busy", time.Hour, true)
	busy.enterDial()
	if busy.TeardownForBudget() != evictionRefused || !busy.started.Load() || busy.idleAsleep.Load() {
		t.Fatal("a dial in flight blocks the eviction and leaves the endpoint awake")
	}
	if !busy.LiveFlows() {
		t.Fatal("LiveFlows must report the dial in flight")
	}
	busy.leaveDial(struct{}{})
	if busy.LiveFlows() {
		t.Fatal("an idle device-less endpoint has no live flows")
	}
	if busy.TeardownForBudget() != evictionDone || !busy.torndown.Load() || busy.started.Load() {
		t.Fatal("an idle awake endpoint is suspended and torn down")
	}
	if snapshot := budget.Snapshot(); strings.Contains(strings.Join(snapshot.Built, ","), "busy") {
		t.Fatalf("an evicted endpoint releases its slot: %+v", snapshot)
	}
}

// A requester being closed stops waiting at once.
func TestBuildBudget_closingRequesterGivesUp(t *testing.T) {
	budget := newTestBudget(t, 1, nil)
	a := builtEndpoint(budget, "a", time.Hour, true)
	a.enterDial()
	c := requester(budget, "c")
	result := make(chan error, 1)
	go func() { result <- acquireWithin(t, budget, c, 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	c.closing.Store(true)
	budget.Notify()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a closing requester must not get a slot")
		}
	case <-time.After(budgetRecheckInterval / 2):
		t.Fatal("closing must wake the waiter at once")
	}
}

// PLAN test 5: two requesters at once and one victim, under the requesters'
// own resumeMu as in resumeOnDial — both get built in turn, no deadlock.
func TestBuildBudget_concurrentRequestersNoDeadlock(t *testing.T) {
	for round := range 50 {
		budget := newTestBudget(t, 1, nil)
		builtEndpoint(budget, "victim", time.Hour, true)
		first := requester(budget, "first")
		second := requester(budget, "second")
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, w := range []*Endpoint{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				w.resumeMu.Lock()
				defer w.resumeMu.Unlock()
				if err := budget.Acquire(ctx, w); err != nil {
					errs <- err
					return
				}
				w.torndown.Store(false)
				w.idleAsleep.Store(false)
				w.started.Store(true)
				w.budgetRegister()
			}()
		}
		waitOrFail(t, &wg, 5*time.Second)
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
		if snapshot := budget.Snapshot(); len(snapshot.Built) != 1 || len(snapshot.Building) != 0 {
			t.Fatalf("round %d: one device at most: %+v", round, snapshot)
		}
	}
}

// Without a cap the budget only keeps the registry.
func TestBuildBudget_noCap(t *testing.T) {
	budget := newTestBudget(t, 0, nil)
	for i := range 5 {
		w := requester(budget, fmt.Sprint("n", i))
		if err := acquireWithin(t, budget, w, time.Second); err != nil {
			t.Fatal(err)
		}
		markRebuilt(w)
	}
	if snapshot := budget.Snapshot(); len(snapshot.Built) != 5 {
		t.Fatalf("registry: %+v", snapshot)
	}
	var nilBudget *BuildBudget
	nilBudget.Register(nil)
	nilBudget.Release(nil)
	nilBudget.Notify()
	if nilBudget.Acquire(context.Background(), nil) != nil || nilBudget.Snapshot().Max != 0 {
		t.Fatal("a nil budget does nothing")
	}
}
