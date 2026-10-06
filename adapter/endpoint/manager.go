package endpoint

import (
	"context"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

var _ adapter.EndpointManager = (*Manager)(nil)

type Manager struct {
	registry      adapter.EndpointRegistry
	access        sync.Mutex
	scope         *adapter.Scope
	endpoints     []adapter.Endpoint
	endpointByTag map[string]adapter.Endpoint
	// lx:begin chain
	// SPEC 073: опции созданных endpoint'ов по тегу — фабрика звеньев цепочки
	// пересоздаёт endpoint из них.
	optionsByTag map[string]managedOptions
	// lx:end chain
	// lx:begin fast-close
	// SPEC 030: each endpoint runs in its own scope so Close can tear them down
	// concurrently (see manager_close_lx.go).
	logger          log.ContextLogger
	endpointScopes  map[adapter.Endpoint]*adapter.Scope
	endpointClosers []*adapter.Scope
	// lx:end fast-close
}

func NewManager(logger log.ContextLogger, registry adapter.EndpointRegistry) *Manager {
	return &Manager{
		registry:       registry,
		endpointByTag:  make(map[string]adapter.Endpoint),
		optionsByTag:   make(map[string]managedOptions), // lx: chain
		logger:         logger,                          // lx: fast-close
		endpointScopes: make(map[adapter.Endpoint]*adapter.Scope),
	}
}

func (m *Manager) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	m.access.Lock()
	defer m.access.Unlock()
	if stage == adapter.StartStateInitialize {
		m.scope = scope
		scope.Add(m.closeEndpoints) // lx: fast-close
	}
	if stage == adapter.StartStateStart {
		return nil
	}
	for _, endpoint := range m.endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		err := m.endpointScope(endpoint).Start(name, endpoint, stage) // lx: fast-close
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) StartEndpoint(endpoint adapter.Endpoint) error {
	m.access.Lock()
	endpointScope := m.endpointScope(endpoint) // lx: fast-close
	m.access.Unlock()
	return endpointScope.Start("endpoint/"+endpoint.Type()+"["+endpoint.Tag()+"]", endpoint, adapter.StartStateStart)
}

func (m *Manager) Endpoints() []adapter.Endpoint {
	m.access.Lock()
	defer m.access.Unlock()
	return m.endpoints
}

func (m *Manager) Get(tag string) (adapter.Endpoint, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	endpoint, found := m.endpointByTag[tag]
	return endpoint, found
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	endpoint, err := m.registry.Create(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	_, loaded := m.endpointByTag[tag]
	if loaded {
		return E.New("duplicate endpoint tag: ", tag)
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointByTag[tag] = endpoint
	m.optionsByTag[tag] = managedOptions{endpointType: outboundType, options: options} // lx: chain
	return nil
}
