package endpoint

import "github.com/sagernet/sing-box/adapter"

// lx: SPEC 073 — память опций endpoint'ов для фабрики звеньев цепочки.

var _ adapter.EndpointOptionsProvider = (*Manager)(nil)

type managedOptions struct {
	endpointType string
	options      any
}

// OptionsOf возвращает тип и опции, из которых был создан endpoint с тегом tag.
func (m *Manager) OptionsOf(tag string) (string, any, bool) {
	m.access.Lock()
	defer m.access.Unlock()
	item, loaded := m.optionsByTag[tag]
	if !loaded {
		return "", nil, false
	}
	return item.endpointType, item.options, true
}
