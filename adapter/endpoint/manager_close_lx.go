package endpoint

// lx: SPEC 030 — close endpoints concurrently instead of one at a time.
//
// Upstream starts every endpoint in a child of the manager's scope, and closing
// that scope runs the children one after another. With the sockets already
// closed by box.Close's pre-pass (router DevicePause), each device teardown is
// fast; running them in parallel turns the serial sum over N endpoints into a
// bounded max. Each endpoint therefore runs in a scope of its own, and the
// manager's scope gets one cleanup that closes all of them under a capped
// task.Group, so N large gVisor netstack teardowns don't spike memory. Run (no
// FastFail) joins every close before returning — a teardown is never
// abandoned, so no receive worker is left holding a freed netstack.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/task"
)

// endpointScope returns the endpoint's own scope, creating it under the
// manager's scope on first use. Called with m.access held.
func (m *Manager) endpointScope(endpoint adapter.Endpoint) *adapter.Scope {
	endpointScope, loaded := m.endpointScopes[endpoint]
	if !loaded {
		endpointScope = adapter.NewScope(m.scope.Context(), m.logger)
		m.endpointScopes[endpoint] = endpointScope
		m.endpointClosers = append(m.endpointClosers, endpointScope)
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
	group := task.Group{}
	group.Concurrency(8)
	for _, closer := range closers {
		group.Append0(func(ctx context.Context) error {
			return closer.Close()
		})
	}
	err := group.Run(context.Background())
	if err != nil {
		return E.Cause(err, "close endpoints")
	}
	return nil
}
