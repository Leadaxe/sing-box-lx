package wireguard

// lx: SPEC 097 — lazy device build and the build budget, endpoint side.
//
// A lazy endpoint is born at SPEC 020 level 3: PostStart does not build the
// device but leaves the endpoint torn down and asleep, and the first dial
// builds it through the ordinary resumeOnDial rebuild path. The only
// difference from "fell asleep and was torn down" is neverBuilt, which exists
// for observability.

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

var _ adapter.IdleStateReporter = (*Endpoint)(nil)

// buildWaitMax bounds the build-budget wait of one rebuild: a dial context
// without a deadline, and the L3-forward path, which has no dial context.
const buildWaitMax = C.TCPTimeout

// initLazyBuild reads lx.wg from the box context: whether this endpoint builds
// lazily, and the per-box build budget when lx.wg.lazy_build or
// lx.wg.build_max is set. A listen-mode endpoint is never lazy — inbound peers
// connect without a dial to build it.
func (w *Endpoint) initLazyBuild(ctx context.Context, options option.WireGuardEndpointOptions) {
	wg := service.PtrFromContext[option.LXResolved](ctx).WGOrZero()
	w.lazy = wg.LazyBuild && options.ListenPort == 0
	if !wg.LazyBuild && wg.BuildMax <= 0 {
		return
	}
	slot := service.PtrFromContext[adapter.LXBuildBudgetSlot](ctx)
	w.budget, _ = slot.Load(func() any {
		return NewBuildBudget(ctx, wg)
	}).(*BuildBudget)
}

// startLazy is Start for a lazy endpoint, under resumeMu. StartStateStart only
// resolves the detour (SPEC 029: a genuine miss still fails at start);
// PostStart leaves the endpoint torn down and asleep, as if it had slept past
// the teardown window, with a baseline idle clock.
func (w *Endpoint) startLazy(stage adapter.StartStage) error {
	switch stage {
	case adapter.StartStateStart:
		return dialer.InitializeDetour(w.outboundDialer)
	case adapter.StartStatePostStart:
		w.neverBuilt.Store(true)
		w.torndown.Store(true)
		w.sleepSince.Store(time.Now().UnixNano())
		w.started.Store(false)
		w.stampActivity()
		// Last: the dial fast path reads idleAsleep first, and must see the rest
		// of the state once it does.
		w.idleAsleep.Store(true)
		w.logger.Info("lx idle: lazy ", w.Tag(), " (device not built)")
	}
	return nil
}

// enterDial marks a dial entry in flight. Pair it with a deferred leaveDial:
//
//	defer w.leaveDial(w.enterDial())
//
// The increment happens before resumeOnDial reads idleAsleep, which is what
// TeardownForBudget relies on (see there).
func (w *Endpoint) enterDial() struct{} {
	w.dialsInFlight.Add(1)
	return struct{}{}
}

func (w *Endpoint) leaveDial(struct{}) {
	w.dialsInFlight.Add(-1)
}

// budgetAcquire takes a build slot for a rebuild. ctx is the dial context (nil
// on the L3-forward path); the wait is capped at buildWaitMax either way.
func (w *Endpoint) budgetAcquire(ctx context.Context) error {
	if w.budget == nil {
		return nil
	}
	if ctx == nil {
		ctx = w.ctx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, buildWaitMax)
	defer cancel()
	return w.budget.Acquire(ctx, w)
}

// budgetRegister records a freshly built device with the budget. Under
// resumeMu. The device counters start at zero, and so does the baseline of the
// budget's liveness sample.
func (w *Endpoint) budgetRegister() {
	w.budgetTransferSum = 0
	w.budget.Register(w)
}

// evictionVerdict is the outcome of TeardownForBudget.
type evictionVerdict uint8

const (
	// evictionDone: the device was torn down and the slot released.
	evictionDone evictionVerdict = iota
	// evictionRefused: not a candidate (not built, closing, not started yet),
	// a dial in flight, or established TCP flows.
	evictionRefused
	// evictionTraffic: refused only because the transfer counters moved past
	// the threshold since the previous sample. A second sample shortly after
	// tells a live UDP flow from the traffic of a finished probe.
	evictionTraffic
)

// liveFlowsLocked is the "live connection is not torn" guard of the budget,
// under resumeMu: a dial in flight, an established TCP flow, or at least
// idleSuspendTransferThreshold bytes since the budget's previous sample of this
// endpoint. The sample is taken here, so each call opens a new window.
func (w *Endpoint) liveFlowsLocked() evictionVerdict {
	if w.dialsInFlight.Load() > 0 || w.endpoint.ActiveTCPFlows() > 0 {
		return evictionRefused
	}
	sum := w.endpoint.TransferTotals()
	delta := sum - w.budgetTransferSum // uint64 wrap on anomaly → huge delta → live
	w.budgetTransferSum = sum
	if delta >= idleSuspendTransferThreshold {
		return evictionTraffic
	}
	return evictionDone
}

// LiveFlows reports whether the endpoint carries live connections by the
// budget's guard (and takes a new transfer sample). For tests and diagnostics;
// the budget itself decides inside TeardownForBudget.
func (w *Endpoint) LiveFlows() bool {
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	return w.liveFlowsLocked() != evictionDone
}

// TeardownForBudget is the forced teardown of a budget victim (SPEC 097). The
// budget picked it without any lock of ours, so candidacy is re-checked here
// under resumeMu: built, not closing, started or asleep, and no live flows. An
// awake victim is suspended first, as SuspendIfIdle would, then torn down.
//
// Lock order: the caller may hold its own resumeMu (a torn-down requester), and
// takes ours; we take budget.mu in Release. Nothing here waits on another
// endpoint.
func (w *Endpoint) TeardownForBudget() evictionVerdict {
	w.resumeMu.Lock()
	defer w.resumeMu.Unlock()
	if w.closing.Load() || w.torndown.Load() {
		return evictionRefused
	}
	awake := w.started.Load()
	if !awake && !w.idleAsleep.Load() {
		// Start has not finished, or the endpoint was stopped: not ours to touch.
		return evictionRefused
	}
	if verdict := w.liveFlowsLocked(); verdict != evictionDone {
		return verdict
	}
	if awake {
		// A dial that read idleAsleep == false before the store below has
		// already incremented dialsInFlight, so the re-check sees it; one that
		// reads it after takes the slow path and waits for resumeMu, then finds
		// the endpoint torn down and rebuilds it. started stays true until the
		// re-check, so no dial is refused as "not ready" meanwhile.
		w.idleAsleep.Store(true)
		if w.dialsInFlight.Load() > 0 {
			w.idleAsleep.Store(false)
			return evictionRefused
		}
		w.started.Store(false)
		w.sleepSince.Store(time.Now().UnixNano())
		w.endpoint.Suspend()
	}
	w.torndown.Store(true)
	w.endpoint.Teardown()
	w.logger.Info("lx idle: teardown ", w.Tag(), " by=budget")
	w.budget.Release(w)
	return evictionDone
}

// IdleState implements adapter.IdleStateReporter: the endpoint's state for
// observability, read from atomics without resumeMu.
func (w *Endpoint) IdleState() adapter.IdleState {
	var state string
	switch {
	case w.building.Load():
		state = adapter.EndpointStateBuilding
	case w.closing.Load():
		state = adapter.EndpointStateDown
	case w.neverBuilt.Load():
		state = adapter.EndpointStateNeverBuilt
	case w.torndown.Load():
		state = adapter.EndpointStateTornDown
	case w.idleAsleep.Load():
		state = adapter.EndpointStateAsleep
	case w.started.Load():
		state = adapter.EndpointStateUp
	default:
		state = adapter.EndpointStateDown
	}
	var idleSince time.Duration
	if w.lastActivity.Load() != 0 {
		idleSince = w.IdleSince()
	}
	return adapter.IdleState{
		State:      state,
		IdleSince:  idleSince,
		SleepSince: w.SleepSince(),
	}
}
