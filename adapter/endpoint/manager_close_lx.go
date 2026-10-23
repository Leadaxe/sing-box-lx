package endpoint

// lx: SPEC 030 — close endpoints concurrently instead of one at a time.
//
// Upstream starts every endpoint in a child of the manager's scope, and closing
// that scope runs the children one after another. box.Close's pre-pass
// (Router.QuiesceForShutdown → each endpoint's QuiesceForShutdown) has already
// marked every WG/AWG endpoint closing and kicked its socket release off, so
// each device teardown is fast; running them in parallel turns the serial sum
// over N endpoints into a bounded max. Each endpoint therefore runs in a scope of its own, and the
// manager's scope gets one cleanup that closes all of them under a capped
// task.Group, so N large gVisor netstack teardowns don't spike memory. Run (no
// FastFail) joins every close before returning — a teardown is never
// abandoned, so no receive worker is left holding a freed netstack.
//
// SPEC 030 §8 (issue #50) — close watchdog. If that join runs past
// closeWatchdogThreshold, the watchdog logs the tags that have not returned
// from Close yet together with a full goroutine dump (common/lxclosewatch),
// once, and the pass keeps waiting: the teardown is still never abandoned.

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxclosewatch"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/task"
)

// closeWatchdogThreshold is how long closeEndpoints may run before the
// watchdog dumps. A variable so the test can shorten it.
var closeWatchdogThreshold = lxclosewatch.DefaultThreshold

// endpointCloser is one endpoint's scope with the tag the watchdog reports.
type endpointCloser struct {
	tag   string
	scope *adapter.Scope
}

// endpointScope returns the endpoint's own scope, creating it under the
// manager's scope on first use. Called with m.access held.
func (m *Manager) endpointScope(endpoint adapter.Endpoint) *adapter.Scope {
	endpointScope, loaded := m.endpointScopes[endpoint]
	if !loaded {
		endpointScope = adapter.NewScope(m.scope.Context(), m.logger)
		m.endpointScopes[endpoint] = endpointScope
		m.endpointClosers = append(m.endpointClosers, endpointCloser{tag: endpoint.Tag(), scope: endpointScope})
	}
	return endpointScope
}

func (m *Manager) closeEndpoints() error {
	m.access.Lock()
	closers := m.endpointClosers
	m.endpointClosers = nil
	m.endpointScopes = make(map[adapter.Endpoint]*adapter.Scope)
	m.access.Unlock()
	if len(closers) == 0 {
		return nil
	}
	var pendingAccess sync.Mutex
	pending := make(map[string]struct{}, len(closers))
	for _, closer := range closers {
		pending[closer.tag] = struct{}{}
	}
	watchdog := lxclosewatch.Start(m.logger, "close endpoints", closeWatchdogThreshold, func() []string {
		pendingAccess.Lock()
		defer pendingAccess.Unlock()
		return slices.Sorted(maps.Keys(pending))
	})
	defer watchdog.Stop()
	group := task.Group{}
	group.Concurrency(8)
	for _, closer := range closers {
		group.Append0(func(ctx context.Context) error {
			err := closer.scope.Close()
			pendingAccess.Lock()
			delete(pending, closer.tag)
			pendingAccess.Unlock()
			return err
		})
	}
	err := group.Run(context.Background())
	if err != nil {
		return E.Cause(err, "close endpoints")
	}
	return nil
}
