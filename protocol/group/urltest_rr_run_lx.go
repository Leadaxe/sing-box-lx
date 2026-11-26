package group

// lx:begin SPEC 019, SPEC 138 the run of a round_robin group in flight
//
// A run of the group (scheduled, manual, after a network change) probes through
// upstream's URLTestOutbounds, which erases the history of a node on one failed
// probe. A batch of the run in which no probe answered, before any probe of the
// run answered, has no control (A0): nothing in it tells a dead node from a dead
// network. Such a batch erases nothing: the run gives the history its probes
// erased back (I5, ALGORITHM §5). Once a later batch answers, the run probes
// those pool members once more, now with that answer as the control.
// The manual test is the exception (an explicit action of the user): its batches
// erase history as upstream, so the node that failed it shows as an error; with
// no answer it still lays out nothing and lifts nothing. The run after a network
// change is no manual test.
// A run in which no probe answered changes nothing of the group: the scheduled
// run lifts the quarantine and the marks and restarts strikes(n) at its first
// answer, not at its start, and a run with no answer lays out no slot.
//
// The run also keeps the nodes struck out (D1) while it is in flight: the
// scheduled run puts none of them into a slot, neither by its fill nor by its
// layout, which gives the hole to the next live node instead.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

// rrRun is the run of the group in flight; it lives in the balancer. Only the
// holder of URLTestGroup.checking begins and ends it.
type rrRun struct {
	access    sync.Mutex
	active    bool           // a run is in flight
	scheduled bool           // the scheduled run: its first answer lifts (rrScheduledRun)
	manual    bool           // the manual test: its batches with no answer erase history as upstream
	answered  bool           // a probe of the run answered
	struck    map[string]int // the D1 of the nodes while the run is in flight
}

// rrRunBegin begins a run; scheduled is the lazy scheduled run, manual the manual
// test. It returns the nodes in quarantine now: the scheduled run probes them
// (§8 item 19), and its first answer lifts them.
func (b *balancer) rrRunBegin(scheduled, manual bool) []string {
	r := &b.run
	r.access.Lock()
	r.active, r.scheduled, r.manual, r.answered, r.struck = true, scheduled, manual, false, nil
	r.access.Unlock()
	if !scheduled {
		return nil
	}
	var quarantined []string
	if set := b.memory.quarantine.Load(); set != nil {
		for tag := range *set {
			quarantined = append(quarantined, tag)
		}
	}
	return quarantined
}

// rrRunAnswered takes an answer of a probe of the run. The first one of the
// scheduled run does what the scheduled run does to the memory (rrScheduledRun),
// before the fill of the answer looks at the quarantine; strikes(n) then counts
// the D1 since the run began. A probe that answers outside a run changes nothing.
func (b *balancer) rrRunAnswered() {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	if !r.active || r.answered {
		return
	}
	r.answered = true
	if !r.scheduled {
		return
	}
	b.rrScheduledRun()
	if len(r.struck) == 0 {
		return
	}
	m := &b.memory
	m.access.Lock()
	m.strikes = make(map[string]int, len(r.struck))
	for tag, strikes := range r.struck {
		m.strikes[tag] = strikes
	}
	m.access.Unlock()
}

// rrRunEnd ends the run and reports whether a probe of it answered.
func (b *balancer) rrRunEnd() bool {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	answered := r.answered
	r.active, r.scheduled, r.manual, r.answered, r.struck = false, false, false, false, nil
	return answered
}

// rrRunGotAnswer reports whether a probe of the run in flight answered.
func (b *balancer) rrRunGotAnswer() bool {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	return r.active && r.answered
}

// rrRunManual reports whether the run in flight is the manual test.
func (b *balancer) rrRunManual() bool {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	return r.active && r.manual
}

// rrRunStrike records a D1 of tag while a run is in flight.
func (b *balancer) rrRunStrike(tag string) {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	if !r.active {
		return
	}
	if r.struck == nil {
		r.struck = make(map[string]int)
	}
	r.struck[tag]++
}

// rrRunStruck reports whether tag was struck out while the run in flight runs.
func (b *balancer) rrRunStruck(tag string) bool {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	return r.active && r.struck[tag] > 0
}

// rrStruckInScheduledRun reports whether tag was struck out while the scheduled
// run in flight runs: that run puts it into no slot (§5).
func (b *balancer) rrStruckInScheduledRun(tag string) bool {
	r := &b.run
	r.access.Lock()
	defer r.access.Unlock()
	return r.active && r.scheduled && r.struck[tag] > 0
}

// rrDropStruck empties, in tags, the slots of the nodes struck out (D1) while the
// scheduled run runs (§5): the run puts none of them back; struck out earlier, a
// node that answered the run is a candidate as any other.
func (b *balancer) rrDropStruck(tags []string) []string {
	for i, tag := range tags {
		if tag != "" && b.rrRunStruck(tag) {
			tags[i] = ""
		}
	}
	return tags
}

// rrBatch is a batch of probes of a round_robin run: the leaves of its members,
// the history its own failed probes erased, and whether a probe of it answered.
// The history storage is one per core: a batch gives back only what its own
// probes erased, never what another group erased meanwhile.
type rrBatch struct {
	start    time.Time
	leaves   map[string]string // leaf → member tag
	nested   []*URLTest
	answered atomic.Bool
	access   sync.Mutex
	erased   map[string]*adapter.URLTestHistory // leaf → the history a failed probe of the batch erased
}

// rrBatchBegin takes the leaves of the members a batch probes; nil outside
// round_robin. A nested urltest member keeps its own rules: its leaves are not
// taken, its own run answering counts as an answer of the batch.
func (g *URLTestGroup) rrBatchBegin(outbounds []adapter.Outbound) *rrBatch {
	if g.balancer == nil {
		return nil
	}
	batch := &rrBatch{start: time.Now(), leaves: make(map[string]string, len(outbounds))}
	for _, detour := range outbounds {
		if nested, isURLTest := detour.(*URLTest); isURLTest {
			batch.nested = append(batch.nested, nested)
			continue
		}
		if leaf := RealTag(detour, N.NetworkTCP); leaf != "" {
			batch.leaves[leaf] = detour.Tag()
		}
	}
	return batch
}

// probeFailedHook is the hook of a failed probe of the batch, called before the
// probe erases the history of leaf: it keeps what the probe erases.
func (g *URLTestGroup) probeFailedHook(batch *rrBatch) func(leaf string) {
	if batch == nil {
		return nil
	}
	return func(leaf string) {
		if _, taken := batch.leaves[leaf]; !taken {
			return
		}
		history := g.history.LoadURLTestHistory(leaf)
		if history == nil {
			return // nothing to erase: erased before, by this group or another one
		}
		batch.access.Lock()
		if batch.erased == nil {
			batch.erased = make(map[string]*adapter.URLTestHistory)
		}
		batch.erased[leaf] = history
		batch.access.Unlock()
	}
}

// takeErased returns member tag → leaf and history for what the failed probes of
// the batch erased.
func (batch *rrBatch) takeErased() map[string]rrBatchLeaf {
	batch.access.Lock()
	defer batch.access.Unlock()
	erased := make(map[string]rrBatchLeaf, len(batch.erased))
	for leaf, history := range batch.erased {
		erased[batch.leaves[leaf]] = rrBatchLeaf{leaf: leaf, history: history}
	}
	return erased
}

type rrBatchLeaf struct {
	leaf    string
	history *adapter.URLTestHistory
}

// probeHook is the probe hook of the batch: it marks the answer, then runs the
// hook of the group.
func (g *URLTestGroup) probeHook(batch *rrBatch) func(tag string) {
	if batch == nil {
		return g.onProbeAliveLx
	}
	return func(tag string) {
		batch.answered.Store(true)
		g.onProbeAliveLx(tag)
	}
}

// rrBatchEnd ends a batch: an answer is an answer of the run; a batch with no
// answer before the first answer of the run gives back the history its own failed
// probes erased (not what another group sharing the leaf erased meanwhile), except to a node struck out meanwhile (its D1 erased it). After
// the first answer the earlier answer is the control: the erasure stays. The
// manual test gives nothing back: the user asked for the state now; the pool
// nodes it erased are unverified (urltest_rr_unverified_lx.go).
func (g *URLTestGroup) rrBatchEnd(batch *rrBatch) {
	if batch == nil {
		return
	}
	answered := batch.answered.Load()
	for _, nested := range batch.nested {
		if history := g.history.LoadURLTestHistory(RealTag(nested, N.NetworkTCP)); history != nil && !history.Time.Before(batch.start) {
			answered = true
		}
	}
	if answered {
		g.balancer.rrRunAnswered()
		return
	}
	if g.balancer.rrRunGotAnswer() {
		return
	}
	if g.balancer.rrRunManual() {
		g.rrMarkUnverified(batch) // no control: the pool nodes it erased are unverified, their slots stay
		return
	}
	for member, erased := range batch.takeErased() {
		if g.balancer.rrRunStruck(member) {
			continue
		}
		g.history.RestoreURLTestHistoryLx(erased.leaf, erased.history)
	}
}

type probeFailedKey struct{}

// withProbeFailed attaches the hook the URL test batch calls for a leaf whose probe
// failed, before the batch erases the history of the leaf at its end; nil detaches the hook of an
// outer group.
func withProbeFailed(ctx context.Context, onFailed func(leaf string)) context.Context {
	return context.WithValue(ctx, probeFailedKey{}, onFailed)
}

// probeFailedLx calls the hook of ctx, if any (SPEC 019).
func probeFailedLx(ctx context.Context, leaf string) {
	if onFailed, _ := ctx.Value(probeFailedKey{}).(func(leaf string)); onFailed != nil {
		onFailed(leaf)
	}
}

// manualRunKey marks the context of the manual test of a group (URLTestGroup.URLTest,
// URLTest.CheckOutbounds); it rides into the runs of nested groups.
type manualRunKey struct{}

func withManualRunLx(ctx context.Context) context.Context {
	return context.WithValue(ctx, manualRunKey{}, true)
}

// manualRunLx reports whether ctx is the context of a manual test.
func manualRunLx(ctx context.Context) bool {
	manual, _ := ctx.Value(manualRunKey{}).(bool)
	return manual
}

// rrHasHistory reports whether the node of the member tag has history now.
func (g *URLTestGroup) rrHasHistory(tag string) bool {
	node, loaded := g.outbound.Outbound(tag)
	return loaded && g.history.LoadURLTestHistory(RealTag(node, N.NetworkTCP)) != nil
}

// lx:end SPEC 019, SPEC 138
