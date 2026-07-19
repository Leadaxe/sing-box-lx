package endpoint

import (
	"context"
	"os"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/task"
)

var _ adapter.EndpointManager = (*Manager)(nil)

type Manager struct {
	logger        log.ContextLogger
	registry      adapter.EndpointRegistry
	access        sync.Mutex
	started       bool
	stage         adapter.StartStage
	endpoints     []adapter.Endpoint
	endpointByTag map[string]adapter.Endpoint
}

func NewManager(logger log.ContextLogger, registry adapter.EndpointRegistry) *Manager {
	return &Manager{
		logger:        logger,
		registry:      registry,
		endpointByTag: make(map[string]adapter.Endpoint),
	}
}

func (m *Manager) Start(stage adapter.StartStage) error {
	m.access.Lock()
	defer m.access.Unlock()
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	if stage == adapter.StartStateStart {
		// started with outbound manager
		return nil
	}
	for _, endpoint := range m.endpoints {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		done := adapter.LogElapsed(m.logger, stage, " ", name)
		err := adapter.LegacyStart(endpoint, stage)
		done()
		if err != nil {
			return E.Cause(err, stage, " ", name)
		}
	}
	return nil
}

func (m *Manager) Close() error {
	m.access.Lock()
	defer m.access.Unlock()
	if !m.started {
		return nil
	}
	m.started = false
	endpoints := m.endpoints
	m.endpoints = nil
	// lx: SPEC 030 — close endpoints concurrently instead of one at a time. With
	// the sockets already closed by box.Close's pre-pass (router DevicePause),
	// each device teardown is fast; running them in parallel turns the serial sum
	// over N endpoints into a bounded max. Concurrency is capped so N large
	// gVisor netstack teardowns don't spike memory. Run (no FastFail) joins every
	// close before returning — we never abandon a teardown, so there is no
	// use-after-free of a netstack a receive worker still holds.
	if len(endpoints) == 0 {
		return nil
	}
	group := task.Group{}
	group.Concurrency(8)
	for _, endpoint := range endpoints {
		endpoint := endpoint
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		group.Append(name, func(ctx context.Context) error {
			done := adapter.LogElapsed(m.logger, "close ", name)
			closeErr := endpoint.Close()
			done()
			if closeErr != nil {
				return E.Cause(closeErr, "close ", name)
			}
			return nil
		})
	}
	return group.Run(context.Background())
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

func (m *Manager) Remove(tag string) error {
	m.access.Lock()
	endpoint, found := m.endpointByTag[tag]
	if !found {
		m.access.Unlock()
		return os.ErrInvalid
	}
	delete(m.endpointByTag, tag)
	index := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
		return it == endpoint
	})
	if index == -1 {
		panic("invalid endpoint index")
	}
	m.endpoints = append(m.endpoints[:index], m.endpoints[index+1:]...)
	started := m.started
	m.access.Unlock()
	if started {
		return endpoint.Close()
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	endpoint, err := m.registry.Create(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.started {
		name := "endpoint/" + endpoint.Type() + "[" + endpoint.Tag() + "]"
		for _, stage := range adapter.ListStartStages {
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(endpoint, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
		}
	}
	if existsEndpoint, loaded := m.endpointByTag[tag]; loaded {
		if m.started {
			err = existsEndpoint.Close()
			if err != nil {
				return E.Cause(err, "close endpoint/", existsEndpoint.Type(), "[", existsEndpoint.Tag(), "]")
			}
		}
		existsIndex := common.Index(m.endpoints, func(it adapter.Endpoint) bool {
			return it == existsEndpoint
		})
		if existsIndex == -1 {
			panic("invalid endpoint index")
		}
		m.endpoints = append(m.endpoints[:existsIndex], m.endpoints[existsIndex+1:]...)
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.endpointByTag[tag] = endpoint
	return nil
}
